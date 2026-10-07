package artifact

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

func testCtx() context.Context {
	return log.WithTarget(context.Background(), log.Discard)
}

type mockArtifact struct {
	name     string
	deps     []Artifact
	includes []Artifact
	inputs   []Input
	identity string
	buildFn  func(BuildContext) ([]Output, error)
	built    bool
	mu       sync.Mutex
}

func (m *mockArtifact) Identity() (objstore.Hash, error) {
	if m.identity == "" {
		h := fmt.Sprintf("%064x", len(m.name))
		return objstore.NewHash(h[:64])
	}
	return objstore.NewHash(m.identity)
}

func (m *mockArtifact) Key() string          { return m.name }
func (m *mockArtifact) Depends() []Artifact  { return m.deps }
func (m *mockArtifact) Includes() []Artifact { return m.includes }
func (m *mockArtifact) Inputs() []Input      { return m.inputs }
func (m *mockArtifact) String() string       { return m.name }

func (m *mockArtifact) OutputRefs(store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	return ResolveOutputRefs(m, store)
}

func (m *mockArtifact) Build(ctx BuildContext) ([]Output, error) {
	m.mu.Lock()
	m.built = true
	m.mu.Unlock()
	if m.buildFn != nil {
		return m.buildFn(ctx)
	}
	return nil, nil
}

func TestGraphEmpty(t *testing.T) {
	g := NewGraph()
	if err := g.Build(); err != nil {
		t.Fatal(err)
	}
	order := g.TopologicalOrder()
	if len(order) != 0 {
		t.Fatalf("expected empty order, got %d", len(order))
	}
}

func TestGraphSingleNode(t *testing.T) {
	g := NewGraph()
	a := &mockArtifact{name: "A", identity: strings.Repeat("a", 64)}
	g.Add(a)
	if err := g.Build(); err != nil {
		t.Fatal(err)
	}
	order := g.TopologicalOrder()
	if len(order) != 1 || order[0].String() != "A" {
		t.Fatalf("unexpected order: %v", order)
	}
}

func TestGraphLinearChain(t *testing.T) {
	g := NewGraph()
	c := &mockArtifact{name: "C", identity: strings.Repeat("c", 64)}
	b := &mockArtifact{name: "B", deps: []Artifact{c}, identity: strings.Repeat("b", 64)}
	a := &mockArtifact{name: "A", deps: []Artifact{b}, identity: strings.Repeat("a", 64)}
	g.Add(a)
	g.Add(b)
	g.Add(c)
	if err := g.Build(); err != nil {
		t.Fatal(err)
	}
	order := g.TopologicalOrder()
	if len(order) != 3 {
		t.Fatalf("expected 3, got %d", len(order))
	}
	if order[0].String() != "C" || order[1].String() != "B" || order[2].String() != "A" {
		t.Fatalf("wrong order: %v", order)
	}
}

func TestGraphCycleDetection(t *testing.T) {
	g := NewGraph()
	a := &mockArtifact{name: "A", identity: strings.Repeat("a", 64)}
	b := &mockArtifact{name: "B", identity: strings.Repeat("b", 64)}
	a.deps = []Artifact{b}
	b.deps = []Artifact{a}
	g.Add(a)
	g.Add(b)
	err := g.Build()
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got: %v", err)
	}
}

func TestGraphMissingDep(t *testing.T) {
	g := NewGraph()
	orphan := &mockArtifact{name: "orphan", identity: strings.Repeat("f", 64)}
	a := &mockArtifact{name: "A", deps: []Artifact{orphan}, identity: strings.Repeat("a", 64)}
	g.Add(a)
	err := g.Build()
	if err == nil {
		t.Fatal("expected missing dep error")
	}
}

func TestEngineBuildOrder(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	var buildOrder []string
	var mu sync.Mutex

	c := &mockArtifact{name: "C", identity: strings.Repeat("c", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		mu.Lock()
		buildOrder = append(buildOrder, "C")
		mu.Unlock()
		return nil, nil
	}}
	b := &mockArtifact{name: "B", deps: []Artifact{c}, identity: strings.Repeat("b", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		mu.Lock()
		buildOrder = append(buildOrder, "B")
		mu.Unlock()
		return nil, nil
	}}
	a := &mockArtifact{name: "A", deps: []Artifact{b}, identity: strings.Repeat("a", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		mu.Lock()
		buildOrder = append(buildOrder, "A")
		mu.Unlock()
		return nil, nil
	}}

	g := NewGraph()
	g.Add(a)
	g.Add(b)
	g.Add(c)

	engine := NewEngine(g, store, 1)
	results, err := engine.Run(testCtx())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("build error for %s: %v", r.Artifact, r.Err)
		}
	}

	cIdx := -1
	bIdx := -1
	aIdx := -1
	for i, name := range buildOrder {
		switch name {
		case "C":
			cIdx = i
		case "B":
			bIdx = i
		case "A":
			aIdx = i
		}
	}
	if cIdx > bIdx || bIdx > aIdx {
		t.Fatalf("wrong build order: %v", buildOrder)
	}
}

func TestEngineFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	b := &mockArtifact{name: "B", identity: strings.Repeat("b", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		return nil, fmt.Errorf("build failed")
	}}
	a := &mockArtifact{name: "A", deps: []Artifact{b}, identity: strings.Repeat("a", 64)}

	g := NewGraph()
	g.Add(a)
	g.Add(b)

	engine := NewEngine(g, store, 1)
	results, err := engine.Run(testCtx())
	if err != nil {
		t.Fatal(err)
	}

	var bFailed, aSkipped bool
	for _, r := range results {
		if r.Artifact.String() == "B" && r.Err != nil {
			bFailed = true
		}
		if r.Artifact.String() == "A" && r.Err != nil {
			aSkipped = true
		}
	}
	if !bFailed {
		t.Fatal("expected B to fail")
	}
	if !aSkipped {
		t.Fatal("expected A to be skipped")
	}
}

func TestEngineCacheHit(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	identity := strings.Repeat("a", 64)
	hash, _ := objstore.NewHash(identity)

	// Store a valid manifest blob (empty outputs)
	manifestBlob := ""
	manifestHash, _ := store.Blobs.Store(strings.NewReader(manifestBlob))
	store.Map.Set(hash, manifestHash, false)

	a := &mockArtifact{name: "A", identity: identity, buildFn: func(ctx BuildContext) ([]Output, error) {
		t.Fatal("should not build - cache hit")
		return nil, nil
	}}

	g := NewGraph()
	g.Add(a)

	engine := NewEngine(g, store, 1)
	results, err := engine.Run(testCtx())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Cached {
		t.Fatal("expected cache hit")
	}
}

func TestEngineParallel(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	a := &mockArtifact{name: "A", identity: strings.Repeat("a", 64)}
	b := &mockArtifact{name: "B", identity: strings.Repeat("b", 64)}
	c := &mockArtifact{name: "C", deps: []Artifact{a, b}, identity: strings.Repeat("c", 64)}

	g := NewGraph()
	g.Add(a)
	g.Add(b)
	g.Add(c)

	engine := NewEngine(g, store, 4)
	results, err := engine.Run(testCtx())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("unexpected error for %s: %v", r.Artifact, r.Err)
		}
	}
}

func TestDiscoverDeduplication(t *testing.T) {
	shared := &mockArtifact{name: "shared", identity: strings.Repeat("s", 64)}
	a := &mockArtifact{name: "A", deps: []Artifact{shared}, identity: strings.Repeat("a", 64)}
	b := &mockArtifact{name: "B", deps: []Artifact{shared}, identity: strings.Repeat("b", 64)}
	root := &mockArtifact{name: "root", deps: []Artifact{a, b}, identity: strings.Repeat("r", 64)}

	g, err := Discover([]Artifact{root})
	if err != nil {
		t.Fatal(err)
	}

	if len(g.nodes) != 4 {
		t.Fatalf("expected 4 nodes (deduped), got %d", len(g.nodes))
	}

	count := 0
	for _, n := range g.nodes {
		if n.artifact.Key() == "shared" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected shared to appear once, got %d", count)
	}
}

func TestDiscoverCycleDetection(t *testing.T) {
	a := &mockArtifact{name: "A", identity: strings.Repeat("a", 64)}
	b := &mockArtifact{name: "B", identity: strings.Repeat("b", 64)}
	a.deps = []Artifact{b}
	b.deps = []Artifact{a}

	_, err := Discover([]Artifact{a})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got: %v", err)
	}
}

func TestDiscoverEngineRun(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	var buildOrder []string
	var mu sync.Mutex

	c := &mockArtifact{name: "C", identity: strings.Repeat("c", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		mu.Lock()
		buildOrder = append(buildOrder, "C")
		mu.Unlock()
		return nil, nil
	}}
	b := &mockArtifact{name: "B", deps: []Artifact{c}, identity: strings.Repeat("b", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		mu.Lock()
		buildOrder = append(buildOrder, "B")
		mu.Unlock()
		return nil, nil
	}}
	a := &mockArtifact{name: "A", deps: []Artifact{b}, identity: strings.Repeat("a", 64), buildFn: func(ctx BuildContext) ([]Output, error) {
		mu.Lock()
		buildOrder = append(buildOrder, "A")
		mu.Unlock()
		return nil, nil
	}}

	g, err := Discover([]Artifact{a})
	if err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(g, store, 1)
	results, err := engine.Run(testCtx())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("build error for %s: %v", r.Artifact, r.Err)
		}
	}

	cIdx, bIdx, aIdx := -1, -1, -1
	for i, name := range buildOrder {
		switch name {
		case "C":
			cIdx = i
		case "B":
			bIdx = i
		case "A":
			aIdx = i
		}
	}
	if cIdx > bIdx || bIdx > aIdx {
		t.Fatalf("wrong build order: %v", buildOrder)
	}
}

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create an artifact that produces outputs
	outputHash1 := strings.Repeat("1", 64)
	outputHash2 := strings.Repeat("2", 64)
	h1, _ := objstore.NewHash(outputHash1)
	h2, _ := objstore.NewHash(outputHash2)

	// Store dummy blobs
	store.Blobs.Store(strings.NewReader("blob1"))
	store.Blobs.Store(strings.NewReader("blob2"))

	a := &mockArtifact{
		name:     "producer",
		identity: strings.Repeat("a1", 32),
		buildFn: func(ctx BuildContext) ([]Output, error) {
			return []Output{
				{Name: "pkg_1.0_amd64.deb", Hash: h1},
				{Name: "control:pkg", Hash: h2},
			}, nil
		},
	}

	g := NewGraph()
	g.Add(a)

	engine := NewEngine(g, store, 1)
	results, err := engine.Run(testCtx())
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("unexpected result: %v", results)
	}

	// Verify outputs stored on node
	outputs := g.OutputsOf(a)
	if len(outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(outputs))
	}
	if outputs[0].Name != "pkg_1.0_amd64.deb" || outputs[0].Hash != h1 {
		t.Fatalf("unexpected output[0]: %+v", outputs[0])
	}
	if outputs[1].Name != "control:pkg" || outputs[1].Hash != h2 {
		t.Fatalf("unexpected output[1]: %+v", outputs[1])
	}

	// Verify manifest was stored in map
	identity, _ := a.Identity()
	if !store.Map.Has(identity) {
		t.Fatal("manifest not stored in map")
	}
}

func TestInputResolution(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	outputHash := strings.Repeat("d", 64)
	h, _ := objstore.NewHash(outputHash)

	producer := &mockArtifact{
		name:     "producer",
		identity: strings.Repeat("ab", 32),
		buildFn: func(ctx BuildContext) ([]Output, error) {
			return []Output{
				{Name: "data.bin", Hash: h},
			}, nil
		},
	}

	consumer := &mockArtifact{
		name:     "consumer",
		identity: strings.Repeat("cd", 32),
		deps:     []Artifact{producer},
		inputs: []Input{
			{Source: producer, Name: "data.bin"},
		},
		buildFn: func(ctx BuildContext) ([]Output, error) {
			resolved, ok := ctx.Inputs["data.bin"]
			if !ok {
				return nil, fmt.Errorf("input data.bin not resolved")
			}
			if resolved != h {
				return nil, fmt.Errorf("expected hash %s, got %s", h, resolved)
			}
			return nil, nil
		},
	}

	g := NewGraph()
	g.Add(producer)
	g.Add(consumer)

	engine := NewEngine(g, store, 1)
	results, err := engine.Run(testCtx())
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("build error for %s: %v", r.Artifact, r.Err)
		}
	}
}

// TestInputResolutionExactMatchOnly verifies resolveInputs requires an exact
// name match: a consumer asking for "libc6.deb" does NOT resolve to a producer
// output named "libc6_2.36-9_amd64.deb". The graph's contract is stable names
// (producers emit "<pkg>.deb"); version-decorated filenames are presentation
// only and live in control metadata, not in input/output plumbing.
func TestInputResolutionExactMatchOnly(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	outputHash := strings.Repeat("d", 64)
	h, _ := objstore.NewHash(outputHash)

	producer := &mockArtifact{
		name:     "producer",
		identity: strings.Repeat("ab", 32),
		buildFn: func(ctx BuildContext) ([]Output, error) {
			return []Output{
				{Name: "libc6_2.36-9_amd64.deb", Hash: h},
			}, nil
		},
	}

	consumer := &mockArtifact{
		name:     "consumer",
		identity: strings.Repeat("cd", 32),
		deps:     []Artifact{producer},
		inputs: []Input{
			{Source: producer, Name: "libc6.deb"},
		},
		buildFn: func(ctx BuildContext) ([]Output, error) {
			t.Fatal("consumer build should not run when input is unresolvable")
			return nil, nil
		},
	}

	g := NewGraph()
	g.Add(producer)
	g.Add(consumer)

	engine := NewEngine(g, store, 1)
	results, err := engine.Run(testCtx())
	if err != nil {
		t.Fatal(err)
	}

	var consumerResult *BuildResult
	for i := range results {
		if results[i].Artifact == consumer {
			consumerResult = &results[i]
		}
	}
	if consumerResult == nil {
		t.Fatal("no result for consumer")
	}
	if consumerResult.Err == nil {
		t.Fatal("expected resolveInputs to fail for non-exact name match, got nil error")
	}
	if !strings.Contains(consumerResult.Err.Error(), "libc6.deb") {
		t.Errorf("error should mention the unresolved input name, got: %v", consumerResult.Err)
	}
}

// TestIncludesCycleAllowed verifies that two artifacts including each other
// (A.Includes() = [B], B.Includes() = [A]) are accepted by the engine — Includes
// edges are closure-only and excluded from cycle detection. Both must build
// successfully without forming a build-order constraint between them.
func TestIncludesCycleAllowed(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	a := &mockArtifact{name: "A", identity: strings.Repeat("a1", 32)}
	b := &mockArtifact{name: "B", identity: strings.Repeat("b2", 32)}
	a.includes = []Artifact{b}
	b.includes = []Artifact{a}

	g, err := Discover([]Artifact{a, b})
	if err != nil {
		t.Fatalf("Discover with Includes-cycle should not error, got: %v", err)
	}
	if g.Len() != 2 {
		t.Fatalf("expected 2 nodes in graph, got %d", g.Len())
	}

	engine := NewEngine(g, store, 2)
	results, err := engine.Run(testCtx())
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("build error for %s: %v", r.Artifact, r.Err)
		}
	}
	if !a.built || !b.built {
		t.Fatalf("expected both A and B to be built, A=%v B=%v", a.built, b.built)
	}
}

// TestDependsCycleStillRejected verifies cycle detection is unchanged: a
// genuine Depends-cycle is still rejected even after Includes was introduced.
func TestDependsCycleStillRejected(t *testing.T) {
	a := &mockArtifact{name: "A", identity: strings.Repeat("a1", 32)}
	b := &mockArtifact{name: "B", identity: strings.Repeat("b2", 32)}
	a.deps = []Artifact{b}
	b.deps = []Artifact{a}

	if _, err := Discover([]Artifact{a, b}); err == nil {
		t.Fatal("expected cycle error from Discover with Depends-cycle, got nil")
	}
}

// TestIncludesNoBuildOrder verifies that Includes does NOT impose ordering:
// if A includes B (and neither has any Depends), they can build concurrently
// — neither blocks the other.
func TestIncludesNoBuildOrder(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	a := &mockArtifact{name: "A", identity: strings.Repeat("a1", 32)}
	b := &mockArtifact{name: "B", identity: strings.Repeat("b2", 32)}
	a.includes = []Artifact{b}

	g, err := Discover([]Artifact{a})
	if err != nil {
		t.Fatal(err)
	}

	// Both A and B must be roots (zero pending) — Includes contributes nothing
	// to the pending counter.
	roots := g.Roots()
	if len(roots) != 2 {
		t.Fatalf("expected both A and B as roots (no build-order edge), got %d roots", len(roots))
	}

	engine := NewEngine(g, store, 2)
	if _, err := engine.Run(testCtx()); err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if !a.built || !b.built {
		t.Fatalf("expected both built; A=%v B=%v", a.built, b.built)
	}
}

// TestConsumerInheritsIncludesClosure verifies the consumer-side closure rule:
// if A.Includes(B) and C.Depends(A), then C also has a build-order edge to B
// even though A itself does not. This is the rule that lets a consumer
// (e.g., rootfs) wait for sibling Includes that it consumes via Inputs().
func TestConsumerInheritsIncludesClosure(t *testing.T) {
	a := &mockArtifact{name: "A", identity: strings.Repeat("a1", 32)}
	b := &mockArtifact{name: "B", identity: strings.Repeat("b2", 32)}
	c := &mockArtifact{name: "C", identity: strings.Repeat("c3", 32)}
	a.includes = []Artifact{b}
	c.deps = []Artifact{a}

	g, err := Discover([]Artifact{c})
	if err != nil {
		t.Fatal(err)
	}

	// A and B must be roots (zero pending). C must wait for BOTH A and B.
	roots := g.Roots()
	if len(roots) != 2 {
		t.Fatalf("expected A and B as roots, got %d", len(roots))
	}

	cNode := g.nodeMap["C"]
	if cNode.pending != 2 {
		t.Fatalf("expected C.pending == 2 (waits for A and B via includes-closure), got %d", cNode.pending)
	}
	cDepKeys := make(map[string]bool)
	for _, dep := range cNode.deps {
		cDepKeys[dep.artifact.Key()] = true
	}
	if !cDepKeys["A"] || !cDepKeys["B"] {
		t.Fatalf("expected C.deps to include both A and B, got %v", cDepKeys)
	}
}

func TestOutputRefs(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Store two real leaf blobs the mock will "produce".
	leaf1, _ := store.Blobs.Store(strings.NewReader("leaf one"))
	leaf2, _ := store.Blobs.Store(strings.NewReader("leaf two"))

	// Multi-output artifact (fan-out).
	multi := &mockArtifact{
		name:     "multi",
		identity: strings.Repeat("d", 64),
		buildFn: func(ctx BuildContext) ([]Output, error) {
			return []Output{
				{Name: "a.deb", Hash: leaf1},
				{Name: "control:a", Hash: leaf2},
			}, nil
		},
	}
	// Single-output artifact (like a Rootfs).
	single := &mockArtifact{
		name:     "single",
		identity: strings.Repeat("e", 64),
		buildFn: func(ctx BuildContext) ([]Output, error) {
			return []Output{{Name: "rootfs.tar.gz", Hash: leaf1}}, nil
		},
	}
	// Never-built artifact.
	unbuilt := &mockArtifact{name: "unbuilt", identity: strings.Repeat("f", 64)}

	g := NewGraph()
	g.Add(multi)
	g.Add(single)
	engine := NewEngine(g, store, 1)
	if _, err := engine.Run(testCtx()); err != nil {
		t.Fatal(err)
	}

	// Multi-output: manifest hash + exactly its two leaves.
	man, leaves, err := multi.OutputRefs(store)
	if err != nil {
		t.Fatalf("multi.OutputRefs: %v", err)
	}
	if man.IsZero() {
		t.Error("manifest hash should be non-zero")
	}
	if len(leaves) != 2 {
		t.Fatalf("multi leaves: got %d, want 2", len(leaves))
	}
	gotLeaves := map[string]bool{leaves[0].String(): true, leaves[1].String(): true}
	if !gotLeaves[leaf1.String()] || !gotLeaves[leaf2.String()] {
		t.Errorf("multi leaves mismatch: %v", leaves)
	}
	// The manifest hash must itself be a stored blob (kept by GC alongside leaves).
	if !store.Blobs.Has(man) {
		t.Error("manifest blob should exist in store")
	}

	// Single-output round-trips too.
	_, singleLeaves, err := single.OutputRefs(store)
	if err != nil {
		t.Fatalf("single.OutputRefs: %v", err)
	}
	if len(singleLeaves) != 1 || !singleLeaves[0].Equal(leaf1) {
		t.Errorf("single leaves: %v", singleLeaves)
	}

	// Unbuilt artifact returns an error (not empty success).
	if _, _, err := unbuilt.OutputRefs(store); err == nil {
		t.Error("unbuilt.OutputRefs should return an error")
	}
}
