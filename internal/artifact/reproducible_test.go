package artifact

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// storeBlob stores s as a blob and returns a named output referencing it.
func storeBlob(t *testing.T, store *objstore.Store, name, content string) Output {
	t.Helper()
	h, err := store.Blobs.Store(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return Output{Name: name, Hash: h}
}

// mapKeyCount returns the number of map entries in the store.
func mapKeyCount(t *testing.T, store *objstore.Store) int {
	t.Helper()
	n := 0
	if err := store.Map.Iterate(func(objstore.Hash) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedBuilt runs a one-node-per-artifact build over the graph so each target's
// output manifest exists in the store, as a reproducibility check requires.
func seedBuilt(t *testing.T, g *Graph, store *objstore.Store) {
	t.Helper()
	engine := NewEngine(g, store, 2)
	results, err := engine.Run(testCtx())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("seed build failed for %s: %v", r.Artifact.Key(), r.Err)
		}
	}
}

// TestCheckReproducibleMatch: a graph whose nodes rebuild to identical outputs is
// fully reproducible, and the check writes log blobs but no new map entries.
func TestCheckReproducibleMatch(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	b := &mockArtifact{name: "B", identity: strings.Repeat("b", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		return []Output{storeBlob(t, ctx.Store, "b.out", "B-content")}, nil
	}}
	a := &mockArtifact{name: "A", deps: []Artifact{b}, identity: strings.Repeat("a", 64),
		inputs: []Input{{Source: b, Name: "b.out"}},
		buildFn: func(ctx BuildContext) ([]Output, error) {
			return []Output{storeBlob(t, ctx.Store, "a.out", "A-content")}, nil
		}}

	g := NewGraph()
	g.Add(a)
	g.Add(b)
	if err := g.Build(); err != nil {
		t.Fatal(err)
	}
	seedBuilt(t, g, store)

	before := mapKeyCount(t, store)

	// Mirror the command's hooks: route each node's log into its own buffer so the
	// runner can store it as a blob and so the compare line is captured.
	bufs := map[string]*log.BufferTarget{}
	var bufMu sync.Mutex
	hooks := ReproHooks{
		NodeStarted: func(key string) context.Context {
			buf := log.NewBufferTarget()
			bufMu.Lock()
			bufs[key] = buf
			bufMu.Unlock()
			return log.WithTarget(testCtx(), buf)
		},
	}

	results, err := CheckReproducible(testCtx(), g, store, []string{"A", "B"}, 2, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s: unexpected error: %v", r.Key, r.Err)
		}
		if !r.Match {
			t.Fatalf("%s: expected match, got mismatch (orig=%s repro=%s)", r.Key, r.OriginalManifest, r.ReproducedManifest)
		}
		if r.Log.IsZero() {
			t.Fatalf("%s: expected a log blob hash", r.Key)
		}
		// The log blob must be reachable by hash even though no map key points at it.
		if _, err := store.Blobs.Open(r.Log); err != nil {
			t.Fatalf("%s: log blob not readable: %v", r.Key, err)
		}
	}

	if after := mapKeyCount(t, store); after != before {
		t.Fatalf("check wrote %d new map entries; expected 0", after-before)
	}

	// The compare line must be the last record in each node's log, tagged reprod.
	for key, buf := range bufs {
		var last log.Record
		r := buf.Reader()
		for {
			rec, rerr := r.Read()
			if rerr == log.ErrNoMore {
				break
			}
			last = rec
		}
		if last.Component != log.Reprod {
			t.Fatalf("%s: last log record component = %v, want reprod", key, last.Component)
		}
		if !strings.Contains(last.Msg, "output matches") {
			t.Fatalf("%s: last log line %q is not the match line", key, last.Msg)
		}
	}
}

// TestCheckReproducibleMismatch: a node whose second build differs is reported as
// a mismatch (not an error).
func TestCheckReproducibleMismatch(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	var n atomic.Int64
	a := &mockArtifact{name: "A", identity: strings.Repeat("a", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		content := "stable"
		if n.Add(1) > 1 {
			content = "drifted"
		}
		return []Output{storeBlob(t, ctx.Store, "a.out", content)}, nil
	}}

	g := NewGraph()
	g.Add(a)
	if err := g.Build(); err != nil {
		t.Fatal(err)
	}
	seedBuilt(t, g, store)

	results, err := CheckReproducible(testCtx(), g, store, []string{"A"}, 1, ReproHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Err != nil {
		t.Fatalf("expected no rebuild error, got %v", r.Err)
	}
	if r.Match {
		t.Fatal("expected mismatch")
	}
	if r.OriginalManifest == r.ReproducedManifest {
		t.Fatal("expected differing manifest hashes")
	}
}

// TestCheckReproducibleBuildError: a node whose rebuild fails is reported as an
// error (red), not a match.
func TestCheckReproducibleBuildError(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	var n atomic.Int64
	a := &mockArtifact{name: "A", identity: strings.Repeat("a", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		if n.Add(1) > 1 {
			return nil, errBoom
		}
		return []Output{storeBlob(t, ctx.Store, "a.out", "ok")}, nil
	}}

	g := NewGraph()
	g.Add(a)
	if err := g.Build(); err != nil {
		t.Fatal(err)
	}
	seedBuilt(t, g, store)

	results, err := CheckReproducible(testCtx(), g, store, []string{"A"}, 1, ReproHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Err == nil {
		t.Fatal("expected a rebuild error")
	}
	if results[0].Match {
		t.Fatal("a failed rebuild must not count as a match")
	}
}

// TestCheckReproducibleRequiresBuilt: a target with no output manifest in the
// store is a hard error before any rebuild runs.
func TestCheckReproducibleRequiresBuilt(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	a := &mockArtifact{name: "A", identity: strings.Repeat("a", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		return []Output{storeBlob(t, ctx.Store, "a.out", "ok")}, nil
	}}
	g := NewGraph()
	g.Add(a)
	if err := g.Build(); err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT seeded: A has never been built.

	_, err = CheckReproducible(testCtx(), g, store, []string{"A"}, 1, ReproHooks{})
	if err == nil {
		t.Fatal("expected an error for an unbuilt target")
	}
	if !strings.Contains(err.Error(), "not built") {
		t.Fatalf("expected a not-built error, got %v", err)
	}
	if a.built {
		t.Fatal("no rebuild should run when a prerequisite is missing")
	}
}

// TestCheckReproducibleInputsFromOriginal: a node's rebuild receives its inputs
// from the dependency's ORIGINAL cached outputs, independent of graph ordering.
func TestCheckReproducibleInputsFromOriginal(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	var seenInput atomic.Value
	b := &mockArtifact{name: "B", identity: strings.Repeat("b", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		return []Output{storeBlob(t, ctx.Store, "b.out", "B-original")}, nil
	}}
	a := &mockArtifact{name: "A", deps: []Artifact{b}, identity: strings.Repeat("a", 64),
		inputs: []Input{{Source: b, Name: "b.out"}},
		buildFn: func(ctx BuildContext) ([]Output, error) {
			if h, ok := ctx.Inputs["b.out"]; ok {
				seenInput.Store(h)
			}
			return []Output{storeBlob(t, ctx.Store, "a.out", "A-content")}, nil
		}}

	g := NewGraph()
	g.Add(a)
	g.Add(b)
	if err := g.Build(); err != nil {
		t.Fatal(err)
	}
	seedBuilt(t, g, store)

	origB := storeBlob(t, store, "b.out", "B-original").Hash

	if _, err := CheckReproducible(testCtx(), g, store, []string{"A"}, 1, ReproHooks{}); err != nil {
		t.Fatal(err)
	}
	got, _ := seenInput.Load().(objstore.Hash)
	if got != origB {
		t.Fatalf("A received input %s; expected the original B output %s", got, origB)
	}
}
