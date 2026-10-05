package importer

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gardenlinux/glbx/internal/objstore"
	"gopkg.in/yaml.v3"
)

// sourcesDoc is the on-disk sources.yml shape: a list of pinned archives, each
// with its filename, content hash, and ordered retrieval URLs.
type sourcesDoc struct {
	Sources []sourcesEntry `yaml:"sources"`
}

type sourcesEntry struct {
	File   string   `yaml:"file"`
	SHA256 string   `yaml:"sha256"`
	URLs   []string `yaml:"urls,omitempty"`
}

// writeSourcesYML writes sources.yml into pkgDir, pinning each orig archive by
// hash and retrieval location. A package with no orig archives (native) writes
// no file, which the build phase reads as "native".
func writeSourcesYML(pkgDir string, entries []SourceEntry) error {
	if len(entries) == 0 {
		return nil
	}

	doc := sourcesDoc{Sources: make([]sourcesEntry, 0, len(entries))}
	for _, e := range entries {
		doc.Sources = append(doc.Sources, sourcesEntry{
			File:   e.Name,
			SHA256: e.Hash.String(),
			URLs:   e.URLs,
		})
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshaling sources.yml: %w", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "sources.yml"), data, 0o644); err != nil {
		return fmt.Errorf("writing sources.yml: %w", err)
	}
	return nil
}

// ParseSourcesYML decodes sources.yml bytes into source entries.
func ParseSourcesYML(data []byte) ([]SourceEntry, error) {
	var doc sourcesDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing sources.yml: %w", err)
	}
	entries := make([]SourceEntry, 0, len(doc.Sources))
	for _, s := range doc.Sources {
		h, err := objstore.NewHash(s.SHA256)
		if err != nil {
			return nil, fmt.Errorf("invalid hash for %s: %w", s.File, err)
		}
		entries = append(entries, SourceEntry{Name: s.File, Hash: h, URLs: s.URLs})
	}
	return entries, nil
}
