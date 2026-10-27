package objstore

import (
	"sort"
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
// Entries are emitted in ascending Name order so a manifest is canonical: the
// same set of outputs always serializes to the same bytes regardless of the
// order a producer happened to assemble them in (e.g. filesystem readdir order),
// which keeps a manifest's hash a function of its contents alone.
func SerializeManifest(outputs []Output) string {
	sorted := make([]Output, len(outputs))
	copy(sorted, outputs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var sb strings.Builder
	for _, out := range sorted {
		sb.WriteString(out.Hash.String())
		sb.WriteByte(' ')
		sb.WriteString(out.Name)
		sb.WriteByte('\n')
	}
	return sb.String()
}
