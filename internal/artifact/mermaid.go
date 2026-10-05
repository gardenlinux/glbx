package artifact

import (
	"fmt"
	"strings"
)

// Mermaid renders the dependency graph as a Mermaid flowchart (top-down).
// Each node is labeled with its Key() and edges represent Depends() relationships.
func (g *Graph) Mermaid() string {
	var sb strings.Builder
	sb.WriteString("graph LR\n")

	// Assign short IDs to nodes for cleaner mermaid output
	ids := make(map[string]string)
	for i, n := range g.nodes {
		key := n.artifact.Key()
		id := fmt.Sprintf("n%d", i)
		ids[key] = id
		sb.WriteString(fmt.Sprintf("    %s[\"%s\"]\n", id, key))
	}

	// Edges: dependency → dependent (dep must build before us, so arrow dep → us)
	for _, n := range g.nodes {
		nID := ids[n.artifact.Key()]
		for _, dep := range n.deps {
			depID := ids[dep.artifact.Key()]
			sb.WriteString(fmt.Sprintf("    %s --> %s\n", depID, nID))
		}
	}

	return sb.String()
}
