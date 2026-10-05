package objstore

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Output is one named blob reference within an artifact's result: a leaf name
// and the blob hash holding its bytes.
type Output struct {
	Name string
	Hash Hash
}

// SerializeManifest encodes outputs as the manifest byte format, one
// "<hash> <name>\n" line per entry. This is the single definition of that
// format, so a manifest reconstructed on a pull-through hit is byte-identical
// to one written directly.
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

// ParseManifest decodes the "<hash> <name>\n" manifest format from r.
func ParseManifest(r io.Reader) ([]Output, error) {
	var outputs []Output
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed manifest line: %q", line)
		}
		hash, err := NewHash(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid hash in manifest: %w", err)
		}
		outputs = append(outputs, Output{Name: parts[1], Hash: hash})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return outputs, nil
}
