package artifact

import (
	"container/heap"
	"fmt"
	"sort"
	"sync"
)

type nodeState int

const (
	statePending nodeState = iota
	stateBuilding
	stateComplete
	stateFailed
	stateSkipped
)

type node struct {
	artifact Artifact
	state    nodeState
	err      error
	deps     []*node
	includes []*node
	rdeps    []*node
	pending  int
	outputs  []Output
}

type Graph struct {
	nodes   []*node
	nodeMap map[string]*node
	built   bool
	mu      sync.Mutex
}

func NewGraph() *Graph {
	return &Graph{
		nodeMap: make(map[string]*node),
	}
}

func (g *Graph) Add(a Artifact) {
	key := a.Key()
	if _, exists := g.nodeMap[key]; exists {
		return
	}
	n := &node{artifact: a}
	g.nodes = append(g.nodes, n)
	g.nodeMap[key] = n
}

func (g *Graph) Build() error {
	if g.built {
		return nil
	}

	// Pass 1: wire Includes for all nodes so the depends-closure walk in pass 2
	// can read each node's includes set.
	for _, n := range g.nodes {
		for _, inc := range n.artifact.Includes() {
			incNode, ok := g.nodeMap[inc.Key()]
			if !ok {
				return fmt.Errorf("artifact %s includes %s which is not in the graph", n.artifact.Key(), inc.Key())
			}
			n.includes = append(n.includes, incNode)
		}
	}

	// Pass 2: wire Depends, plus the consumer-side closure across Includes. If
	// C.Depends(A) and A.Includes(B), then C also gets an ordering edge to B
	// (an Includes edge carries zero build order for the includer A itself, so
	// without this a consumer would not wait on a sibling it consumes). And
	// crucially, once the walk crosses an Includes edge into B, it continues
	// through B's *Depends* too: B.Depends(D) means C's install-check resolves
	// D (walkBinaries follows both includes and extraDeps), yet nothing orders D
	// before C otherwise — the C→B hop is an Include (no order) so the ordinary
	// B→D chain never reaches C. Bridging that one Include→Depends boundary is
	// the fix.
	//
	// What the walk must NOT do is flatten ordinary Depends→Depends chains: for
	// a plain C→A→D the engine already orders D before A before C via pending
	// counts, so a direct C→D edge would be redundant (and would bloat the
	// generated CI needs: lists, which are built from these edges). So we follow
	// a reached node's Depends only once we have crossed an Includes edge
	// (viaInclude), never along a pure-Depends spine.
	//
	// seen is keyed by (node, viaInclude): a node first reached on a pure-Depends
	// spine may later be reached through an Include, and must then be revisited
	// so its Depends get pulled into the closure. A itself never gets an edge to
	// its own includes (the walk is seeded only from a consumer's Depends), so
	// the sibling-Includes cycle stays broken. Identity is unaffected — it folds
	// Depends() only. n.deps is populated as pass 2 runs, so read each reached
	// node's direct Depends via its artifact (through nodeMap), never the
	// half-built n.deps slice.
	for _, n := range g.nodes {
		type visit struct {
			node       *node
			viaInclude bool
		}
		seen := make(map[visit]bool)
		var addEdge func(target *node, viaInclude bool) error
		addEdge = func(target *node, viaInclude bool) error {
			v := visit{target, viaInclude}
			if seen[v] {
				return nil
			}
			seen[v] = true
			// Add the ordering edge once (the first time this node is reached by
			// any path); a later revisit only continues the walk.
			if !seen[visit{target, !viaInclude}] {
				n.deps = append(n.deps, target)
				target.rdeps = append(target.rdeps, n)
				n.pending++
			}
			for _, inc := range target.includes {
				if err := addEdge(inc, true); err != nil {
					return err
				}
			}
			if viaInclude {
				for _, dep := range target.artifact.Depends() {
					depNode, ok := g.nodeMap[dep.Key()]
					if !ok {
						return fmt.Errorf("artifact %s depends on %s which is not in the graph", target.artifact.Key(), dep.Key())
					}
					if err := addEdge(depNode, true); err != nil {
						return err
					}
				}
			}
			return nil
		}
		for _, dep := range n.artifact.Depends() {
			depNode, ok := g.nodeMap[dep.Key()]
			if !ok {
				return fmt.Errorf("artifact %s depends on %s which is not in the graph", n.artifact.Key(), dep.Key())
			}
			if err := addEdge(depNode, false); err != nil {
				return err
			}
		}
	}

	if err := g.detectCycles(); err != nil {
		return err
	}

	g.built = true

	return nil
}

// Discover recursively walks Depends() and Includes() starting from roots,
// deduplicates by Key(), and wires Depends edges plus the consumer-side closure
// across Includes: a Depends edge to X adds ordering edges to X's Includes and,
// once an Includes edge is crossed, onward through those includes' own Depends
// — so a consumer is ordered after the parts of its runtime/install closure
// that an Includes hop would otherwise hide (matching walkBinaries). Ordinary
// Depends→Depends chains are NOT flattened: the engine already orders them via
// pending counts. Includes contribute zero ordering for the includer itself
// (no rdeps/pending edge from the includer to its own include) and are excluded
// from cycle detection. Returns a fully built Graph.
func Discover(roots []Artifact) (*Graph, error) {
	g := &Graph{
		nodeMap: make(map[string]*node),
	}

	var discover func(a Artifact) *node
	discover = func(a Artifact) *node {
		key := a.Key()
		if n, exists := g.nodeMap[key]; exists {
			return n
		}
		n := &node{artifact: a}
		g.nodes = append(g.nodes, n)
		g.nodeMap[key] = n

		// Discover Includes first so that depNode.includes is populated by the
		// time we walk the includes-closure during Depends wiring below.
		for _, inc := range a.Includes() {
			incNode := discover(inc)
			n.includes = append(n.includes, incNode)
		}

		// seen is keyed by (node, viaInclude); see Graph.Build pass 2 for the
		// rationale. We follow a reached node's Depends only once an Includes
		// edge has been crossed (viaInclude), never along a pure-Depends spine.
		type visit struct {
			node       *node
			viaInclude bool
		}
		seen := make(map[visit]bool)
		var addEdge func(target *node, viaInclude bool)
		addEdge = func(target *node, viaInclude bool) {
			v := visit{target, viaInclude}
			if seen[v] {
				return
			}
			seen[v] = true
			if !seen[visit{target, !viaInclude}] {
				n.deps = append(n.deps, target)
				target.rdeps = append(target.rdeps, n)
				n.pending++
			}
			for _, inc := range target.includes {
				addEdge(inc, true)
			}
			if viaInclude {
				for _, dep := range target.artifact.Depends() {
					if depNode, ok := g.nodeMap[dep.Key()]; ok {
						addEdge(depNode, true)
					}
				}
			}
		}
		for _, dep := range a.Depends() {
			depNode := discover(dep)
			addEdge(depNode, false)
		}
		return n
	}

	for _, root := range roots {
		discover(root)
	}

	if err := g.detectCycles(); err != nil {
		return nil, err
	}

	g.built = true
	return g, nil
}

// OutputsOf returns the outputs produced by the given artifact after it has
// been built (or restored from cache). Returns nil if the artifact has not
// been completed yet or is not in the graph.
func (g *Graph) OutputsOf(a Artifact) []Output {
	g.mu.Lock()
	defer g.mu.Unlock()
	n, ok := g.nodeMap[a.Key()]
	if !ok {
		return nil
	}
	return n.outputs
}

func (g *Graph) detectCycles() error {
	type color int
	const (
		white color = iota
		gray
		black
	)

	colors := make(map[*node]color)
	var stack []string

	var visit func(n *node) error
	visit = func(n *node) error {
		colors[n] = gray
		stack = append(stack, n.artifact.Key())
		for _, dep := range n.deps {
			switch colors[dep] {
			case gray:
				stack = append(stack, dep.artifact.Key())
				return fmt.Errorf("cycle detected: %v", stack)
			case white:
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		colors[n] = black
		return nil
	}

	for _, n := range g.nodes {
		if colors[n] == white {
			if err := visit(n); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *Graph) TopologicalOrder() []Artifact {
	visited := make(map[*node]bool)
	var result []Artifact

	var visit func(n *node)
	visit = func(n *node) {
		if visited[n] {
			return
		}
		visited[n] = true
		for _, dep := range n.deps {
			visit(dep)
		}
		result = append(result, n.artifact)
	}

	for _, n := range g.nodes {
		visit(n)
	}
	return result
}

// nodeHeap is a min-heap ordered by artifact Key() for stable topo sort.
type nodeHeap []*node

func (h nodeHeap) Len() int           { return len(h) }
func (h nodeHeap) Less(i, j int) bool { return h[i].artifact.Key() < h[j].artifact.Key() }
func (h nodeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x any)        { *h = append(*h, x.(*node)) }
func (h *nodeHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// StableTopologicalOrder returns artifacts in dependency order with lexicographic
// tiebreaking by Key(). Dependencies always appear before dependents. Among nodes
// with no ordering constraint, they are sorted alphabetically.
func (g *Graph) StableTopologicalOrder() []Artifact {
	inDeg := make(map[*node]int, len(g.nodes))
	for _, n := range g.nodes {
		inDeg[n] = len(n.deps)
	}

	h := &nodeHeap{}
	heap.Init(h)
	for _, n := range g.nodes {
		if inDeg[n] == 0 {
			heap.Push(h, n)
		}
	}

	var result []Artifact
	for h.Len() > 0 {
		n := heap.Pop(h).(*node)
		result = append(result, n.artifact)
		for _, rdep := range n.rdeps {
			inDeg[rdep]--
			if inDeg[rdep] == 0 {
				heap.Push(h, rdep)
			}
		}
	}
	return result
}

func (g *Graph) Roots() []*node {
	var roots []*node
	for _, n := range g.nodes {
		if n.pending == 0 {
			roots = append(roots, n)
		}
	}
	return roots
}

// Len returns the number of nodes in the graph.
func (g *Graph) Len() int {
	return len(g.nodes)
}

// Prune restricts the graph to the given node and its dependency closure
// (everything reachable through deps and includes), dropping all other nodes.
// It must be called after Build. Dependents of kept nodes that fall outside the
// closure are removed from the kept nodes' rdeps so no dangling edges remain.
// A target not in the graph is a no-op returning an error.
func (g *Graph) Prune(targetKey string) error {
	root, ok := g.nodeMap[targetKey]
	if !ok {
		return fmt.Errorf("prune target %s not in graph", targetKey)
	}

	keep := make(map[*node]bool)
	var walk func(n *node)
	walk = func(n *node) {
		if keep[n] {
			return
		}
		keep[n] = true
		for _, d := range n.deps {
			walk(d)
		}
		for _, inc := range n.includes {
			walk(inc)
		}
	}
	walk(root)

	g.nodes = g.nodes[:0]
	g.nodeMap = make(map[string]*node, len(keep))
	for n := range keep {
		g.nodes = append(g.nodes, n)
		g.nodeMap[n.artifact.Key()] = n
	}
	// Drop rdeps that point outside the kept set so the engine's completion
	// bookkeeping (which fans out to rdeps) stays within the pruned graph.
	for _, n := range g.nodes {
		kept := n.rdeps[:0]
		for _, rd := range n.rdeps {
			if keep[rd] {
				kept = append(kept, rd)
			}
		}
		n.rdeps = kept
	}
	return nil
}

// Find returns the artifact in the graph with the given Key(), or nil if no
// such artifact exists.
func (g *Graph) Find(key string) Artifact {
	if n, ok := g.nodeMap[key]; ok {
		return n.artifact
	}
	return nil
}

// Keys returns all artifact keys in the graph in insertion order.
func (g *Graph) Keys() []string {
	keys := make([]string, 0, len(g.nodes))
	for _, n := range g.nodes {
		keys = append(keys, n.artifact.Key())
	}
	return keys
}

// Edge is a built-from dependency: the artifact with Key From must build before
// the one with Key To.
type Edge struct {
	From string
	To   string
}

// Edges returns the graph's built-from edges (one per Depends relationship,
// including the consumer-side includes-closure edges the graph wires), sorted
// lexicographically by (To, From). The order is deterministic across runs and
// machines so a serialized export is stable.
func (g *Graph) Edges() []Edge {
	var edges []Edge
	for _, n := range g.nodes {
		to := n.artifact.Key()
		for _, dep := range n.deps {
			edges = append(edges, Edge{From: dep.artifact.Key(), To: to})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].From < edges[j].From
	})
	return edges
}
