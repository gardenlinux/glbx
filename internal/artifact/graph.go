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
				return fmt.Errorf("artifact %s includes %s which is not in the graph", n.artifact, inc)
			}
			n.includes = append(n.includes, incNode)
		}
	}

	// Pass 2: wire Depends and propagate includes-closure as additional ordering
	// edges for the consumer. If A.Includes(B) and C.Depends(A), then C also gets
	// an ordering edge to B (consumer-side closure), but A itself does NOT get an
	// edge to B (A doesn't wait for its own includes).
	for _, n := range g.nodes {
		seen := make(map[*node]bool)
		var addEdge func(target *node)
		addEdge = func(target *node) {
			if seen[target] {
				return
			}
			seen[target] = true
			n.deps = append(n.deps, target)
			target.rdeps = append(target.rdeps, n)
			n.pending++
			for _, inc := range target.includes {
				addEdge(inc)
			}
		}
		for _, dep := range n.artifact.Depends() {
			depNode, ok := g.nodeMap[dep.Key()]
			if !ok {
				return fmt.Errorf("artifact %s depends on %s which is not in the graph", n.artifact, dep)
			}
			addEdge(depNode)
		}
	}

	if err := g.detectCycles(); err != nil {
		return err
	}

	g.built = true

	return nil
}

// Discover recursively walks Depends() and Includes() starting from roots,
// deduplicates by Key(), wires Depends edges (with consumer-side includes
// closure: a Depends edge to X also adds ordering edges to X's transitive
// Includes), records Includes for closure tracking, and detects cycles.
// Includes themselves contribute zero ordering for the includer (no rdeps/
// pending edge from the includer to its include) and are excluded from cycle
// detection. Returns a fully built Graph.
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

		seen := make(map[*node]bool)
		var addEdge func(target *node)
		addEdge = func(target *node) {
			if seen[target] {
				return
			}
			seen[target] = true
			n.deps = append(n.deps, target)
			target.rdeps = append(target.rdeps, n)
			n.pending++
			for _, inc := range target.includes {
				addEdge(inc)
			}
		}
		for _, dep := range a.Depends() {
			depNode := discover(dep)
			addEdge(depNode)
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
		stack = append(stack, n.artifact.String())
		for _, dep := range n.deps {
			switch colors[dep] {
			case gray:
				stack = append(stack, dep.artifact.String())
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
