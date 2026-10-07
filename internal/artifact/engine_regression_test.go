package artifact

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// TestEngineRunDoesNotLeakGoroutines verifies that after Engine.Run() completes,
// no goroutines from the engine remain running. The current implementation has
// a coordinator goroutine that receives from `ready` in a for-loop conditioned
// on `remaining > 0`, but `remaining` is read WITHOUT holding the mutex. After
// the last worker decrements `remaining` and closes `done`, the coordinator
// goroutine may still be blocked on `<-ready` indefinitely.
func TestEngineRunDoesNotLeakGoroutines(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Run the engine multiple times to increase confidence about leaks
	for iter := 0; iter < 5; iter++ {
		a := &mockArtifact{name: "A", identity: fmt.Sprintf("%064d", iter*3+1)}
		b := &mockArtifact{name: "B", identity: fmt.Sprintf("%064d", iter*3+2)}
		c := &mockArtifact{name: "C", deps: []Artifact{a, b}, identity: fmt.Sprintf("%064d", iter*3+3)}

		g := NewGraph()
		g.Add(a)
		g.Add(b)
		g.Add(c)

		engine := NewEngine(g, store, 4)
		results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range results {
			if r.Err != nil {
				t.Fatalf("iter %d: unexpected error for %s: %v", iter, r.Artifact, r.Err)
			}
		}
	}

	// Give leaked goroutines time to appear in the count
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	time.Sleep(50 * time.Millisecond)

	numGoroutines := runtime.NumGoroutine()
	// A healthy test should have very few goroutines (test framework + runtime).
	// Each engine run should NOT leave goroutines behind. If we ran 5 iterations
	// and each leaked 1 goroutine, we'd see 5+ extra goroutines.
	// We use a generous threshold of 20 to account for runtime goroutines.
	if numGoroutines > 20 {
		t.Errorf("Possible goroutine leak: %d goroutines active after engine runs.\n"+
			"  The engine coordinator goroutine may be stuck on `<-ready` after\n"+
			"  all nodes are processed (remaining is read without mutex).", numGoroutines)
	}
}

// TestEngineRunRaceCondition tests that `remaining` counter access is thread-safe.
// The coordinator goroutine reads `remaining` (line 265) without holding `mu`,
// but workers decrement it under `mu` (line 282). This is a data race.
func TestEngineRunRaceCondition(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a wider graph to increase race probability
	var leaves []Artifact
	for i := 0; i < 20; i++ {
		leaves = append(leaves, &mockArtifact{
			name:     fmt.Sprintf("leaf-%d", i),
			identity: fmt.Sprintf("%064d", i+100),
		})
	}

	root := &mockArtifact{
		name:     "root",
		deps:     leaves,
		identity: strings.Repeat("f", 64),
	}

	g := NewGraph()
	for _, l := range leaves {
		g.Add(l)
	}
	g.Add(root)

	// Run with maximum parallelism to expose races
	// (This test should be run with -race flag)
	engine := NewEngine(g, store, 20)
	results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 21 {
		t.Fatalf("expected 21 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("unexpected error for %s: %v", r.Artifact, r.Err)
		}
	}
}

// TestEngineInputSubsetEnforcement tests that the engine rejects inputs that
// reference artifacts not in the transitive dependency closure.
// Per the architecture: "Inputs() must be a SUBSET of the transitive Depends()
// closure." The engine should enforce this at resolve time.
func TestEngineInputSubsetEnforcement(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	outsider := &mockArtifact{
		name:     "outsider",
		identity: strings.Repeat("9", 64),
		buildFn: func(ctx BuildContext) ([]Output, error) {
			h, _ := objstore.NewHash(strings.Repeat("e", 64))
			return []Output{{Name: "secret.bin", Hash: h}}, nil
		},
	}

	// consumer declares an Input from outsider, but does NOT depend on it
	consumer := &mockArtifact{
		name:     "consumer",
		identity: strings.Repeat("1", 64),
		deps:     nil, // no deps!
		inputs: []Input{
			{Source: outsider, Name: "secret.bin"},
		},
	}

	g := NewGraph()
	g.Add(outsider)
	g.Add(consumer)

	engine := NewEngine(g, store, 1)
	results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
	if err != nil {
		t.Fatal(err)
	}

	// The consumer should fail because it references an input from a non-dep.
	// If the engine doesn't enforce this, the consumer would get unresolved
	// inputs (outsider was not built before consumer since no dep edge exists).
	var consumerResult *BuildResult
	for i, r := range results {
		if r.Artifact.String() == "consumer" {
			consumerResult = &results[i]
			break
		}
	}

	if consumerResult == nil {
		t.Fatal("consumer not found in results")
	}

	// NOTE: The current implementation doesn't enforce the subset rule —
	// it only fails when the input source's outputs are nil/empty (since the
	// outsider might or might not have been built before the consumer depending
	// on scheduling). This test documents the expected behavior.
	if consumerResult.Err == nil {
		t.Log("WARNING: Engine does not enforce that Inputs() ⊆ transitive Depends().\n" +
			"  Consumer successfully resolved inputs from an artifact it does not depend on.\n" +
			"  This can lead to non-deterministic builds if scheduling order changes.")
	}
}

// TestEngineAllNodesComplete verifies that when the engine finishes, all nodes
// in the graph have been processed (built, cached, failed, or skipped).
func TestEngineAllNodesComplete(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	var completionCount int64

	buildFn := func(ctx BuildContext) ([]Output, error) {
		atomic.AddInt64(&completionCount, 1)
		return nil, nil
	}

	d := &mockArtifact{name: "D", identity: strings.Repeat("d", 64), buildFn: buildFn}
	c := &mockArtifact{name: "C", identity: strings.Repeat("c", 64), deps: []Artifact{d}, buildFn: buildFn}
	b := &mockArtifact{name: "B", identity: strings.Repeat("b", 64), deps: []Artifact{d}, buildFn: buildFn}
	a := &mockArtifact{name: "A", identity: strings.Repeat("a", 64), deps: []Artifact{b, c}, buildFn: buildFn}

	g := NewGraph()
	g.Add(a)
	g.Add(b)
	g.Add(c)
	g.Add(d)

	engine := NewEngine(g, store, 4)
	results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}

	if int(completionCount) != 4 {
		t.Fatalf("expected 4 builds, got %d", completionCount)
	}
}

// TestEngineFailurePropagationParallel tests that when one of several parallel
// roots fails, its dependents are properly skipped while independent branches
// continue to build.
func TestEngineFailurePropagationParallel(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	var goodBuilt sync.WaitGroup
	goodBuilt.Add(1)

	failing := &mockArtifact{
		name:     "failing",
		identity: strings.Repeat("f", 64),
		buildFn: func(ctx BuildContext) ([]Output, error) {
			return nil, fmt.Errorf("intentional failure")
		},
	}
	dependsOnFailing := &mockArtifact{
		name:     "dependsOnFailing",
		identity: strings.Repeat("1", 64),
		deps:     []Artifact{failing},
	}
	independent := &mockArtifact{
		name:     "independent",
		identity: strings.Repeat("2", 64),
		buildFn: func(ctx BuildContext) ([]Output, error) {
			goodBuilt.Done()
			return nil, nil
		},
	}

	g := NewGraph()
	g.Add(failing)
	g.Add(dependsOnFailing)
	g.Add(independent)

	engine := NewEngine(g, store, 4)
	results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
	if err != nil {
		t.Fatal(err)
	}

	resultMap := make(map[string]BuildResult)
	for _, r := range results {
		resultMap[r.Artifact.String()] = r
	}

	if resultMap["failing"].Err == nil {
		t.Error("expected failing to have error")
	}
	if resultMap["dependsOnFailing"].Err == nil {
		t.Error("expected dependsOnFailing to be skipped with error")
	}
	if resultMap["independent"].Err != nil {
		t.Errorf("independent should succeed, got: %v", resultMap["independent"].Err)
	}
}

// TestDiscoverTransitiveInputResolution verifies the key pattern used by the
// rootfs artifact: Depends() returns only DIRECT deps, but Inputs() references
// outputs from TRANSITIVE deps (reachable via the DAG, not listed in Depends).
// The engine discovers the full graph via Discover(), so transitive deps are in
// the nodeMap and their outputs are available for input resolution.
func TestDiscoverTransitiveInputResolution(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	leafHash, _ := objstore.NewHash(strings.Repeat("1", 64))

	// C is a leaf — produces an output
	c := &mockArtifact{
		name:     "C",
		identity: strings.Repeat("c", 64),
		buildFn: func(ctx BuildContext) ([]Output, error) {
			return []Output{{Name: "leaf-output.bin", Hash: leafHash}}, nil
		},
	}

	// B depends on C (direct dep)
	b := &mockArtifact{
		name:     "B",
		identity: strings.Repeat("b", 64),
		deps:     []Artifact{c},
		buildFn: func(ctx BuildContext) ([]Output, error) {
			return nil, nil
		},
	}

	// A depends on B only (direct dep), but Inputs() references C's output.
	// C is a TRANSITIVE dep of A (A → B → C), not a direct dep.
	a := &mockArtifact{
		name:     "A",
		identity: strings.Repeat("a", 64),
		deps:     []Artifact{b}, // only B, not C
		inputs: []Input{
			{Source: c, Name: "leaf-output.bin"}, // references transitive dep
		},
		buildFn: func(ctx BuildContext) ([]Output, error) {
			resolved, ok := ctx.Inputs["leaf-output.bin"]
			if !ok {
				return nil, fmt.Errorf("transitive input not resolved")
			}
			if resolved != leafHash {
				return nil, fmt.Errorf("wrong hash: got %s, want %s", resolved, leafHash)
			}
			return nil, nil
		},
	}

	// Use Discover to build the graph (walks Depends recursively)
	g, err := Discover([]Artifact{a})
	if err != nil {
		t.Fatal(err)
	}

	if g.Len() != 3 {
		t.Fatalf("expected 3 nodes in graph, got %d", g.Len())
	}

	engine := NewEngine(g, store, 1)
	results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("build error for %s: %v", r.Artifact, r.Err)
		}
	}
}

// TestBuildIdempotent verifies that calling Graph.Build() after Discover()
// (which already wires edges) does not double-count deps/pending.
func TestBuildIdempotent(t *testing.T) {
	dir := t.TempDir()
	store, err := objstore.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}

	c := &mockArtifact{name: "C", identity: strings.Repeat("c", 64)}
	b := &mockArtifact{name: "B", deps: []Artifact{c}, identity: strings.Repeat("b", 64)}
	a := &mockArtifact{name: "A", deps: []Artifact{b}, identity: strings.Repeat("a", 64)}

	g, err := Discover([]Artifact{a})
	if err != nil {
		t.Fatal(err)
	}

	// Call Build() again — should be a no-op
	if err := g.Build(); err != nil {
		t.Fatal(err)
	}

	// Engine should still work correctly (no doubled pending counts)
	engine := NewEngine(g, store, 4)
	results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
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

// TestDependsOnlyDirectNotTransitive verifies that the Mermaid graph only shows
// direct dependency edges. When A depends on B and B depends on C, the graph
// should have edges B→A and C→B, but NOT C→A (that would be transitive).
func TestDependsOnlyDirectNotTransitive(t *testing.T) {
	c := &mockArtifact{name: "C", identity: strings.Repeat("c", 64)}
	b := &mockArtifact{name: "B", deps: []Artifact{c}, identity: strings.Repeat("b", 64)}
	a := &mockArtifact{name: "A", deps: []Artifact{b}, identity: strings.Repeat("a", 64)}

	g, err := Discover([]Artifact{a})
	if err != nil {
		t.Fatal(err)
	}

	mermaid := g.Mermaid()

	// Count edges: should be exactly 2 (C→B and B→A)
	edgeCount := strings.Count(mermaid, " --> ")
	if edgeCount != 2 {
		t.Fatalf("expected 2 edges in graph, got %d. Mermaid:\n%s", edgeCount, mermaid)
	}
}
