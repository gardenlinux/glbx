package objstore

import (
	"strings"
)

// Output is one named blob reference within an artifact's result: a leaf name
// and the blob hash holding its bytes.
type Output struct {
	Name string
	Hash Hash
}

// SerializeManifest encodes outputs as the manifest byte format, one
// "<hash> <name>\n" line per entry. It is the single definition of that format.
func SerializeManifest(outputs []Output) string {
	var sb strings.Builder
	for _, out := range outputs {
		sb.WriteString(out.Hash.String())
		sb.WriteByte(' ')
		sb.WriteString(out.Name)
		sb.WriteByte('\n')
	}
	return sb.String()
}
