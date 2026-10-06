// Package index implements an in-memory binary package index parsed from
// deb822-format Packages files. It supports lookup by real package name and
// by virtual package name (via Provides), and identification of "core"
// packages (Essential: yes or Priority: required).
package index

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gardenlinux/glbx/internal/debian/deb822"
	"github.com/gardenlinux/glbx/internal/debian/depends"
)

// Package represents a single binary package entry from a Packages index.
type Package struct {
	Name         string
	Version      string
	Architecture string
	Essential    bool
	Priority     string
	Depends      depends.DependencyList
	PreDepends   depends.DependencyList
	Conflicts    depends.DependencyList
	Provides     depends.DependencyList
	Breaks       depends.DependencyList
	Stanza       deb822.Stanza
	SHA256       string
	// SHA1 is computed from the fetched .deb bytes, not read from the index
	// (the Packages format does not require a SHA1 field). It keys the file in
	// the snapshot archive.
	SHA1     string
	Filename string
	Size     int64
}

// Index holds an in-memory representation of a binary package index,
// optimized for dependency resolution lookups.
type Index struct {
	packages  map[string]*Package   // name -> package (latest or last-seen wins)
	providers map[string][]*Package // virtual name -> packages that Provide it
	all       []*Package            // all packages in insertion order
}

// New creates an empty Index.
func New() *Index {
	return &Index{
		packages:  make(map[string]*Package),
		providers: make(map[string][]*Package),
	}
}

// Load parses a deb822-format Packages file from r and returns a populated Index.
// It pre-parses all dependency fields for efficient resolver use.
func Load(r io.Reader) (*Index, error) {
	idx := &Index{
		packages:  make(map[string]*Package),
		providers: make(map[string][]*Package),
	}

	reader := deb822.NewReader(r)
	for {
		stanza, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("index: parsing Packages file: %w", err)
		}

		pkg, err := ParsePackageFromStanza(stanza)
		if err != nil {
			return nil, fmt.Errorf("index: parsing package stanza: %w", err)
		}

		if existing, ok := idx.packages[pkg.Name]; ok {
			return nil, fmt.Errorf("index: duplicate package %q (versions %s and %s)", pkg.Name, existing.Version, pkg.Version)
		}

		idx.packages[pkg.Name] = pkg
		idx.all = append(idx.all, pkg)

		// Index providers for virtual package lookup.
		for _, alt := range pkg.Provides {
			for _, dep := range alt {
				if dep.Name != "" {
					idx.providers[dep.Name] = append(idx.providers[dep.Name], pkg)
				}
			}
		}
	}

	return idx, nil
}

// Get returns the package with the given name, or nil if not found.
func (idx *Index) Get(name string) *Package {
	return idx.packages[name]
}

// SourceName returns the source-package name for this binary package. It
// reads the Source: stanza field, strips an optional "(version)" suffix, and
// falls back to the binary package name when the field is absent (per
// Debian policy: a binary with no Source: line was built from a source of
// the same name).
func (p *Package) SourceName() string {
	s := strings.TrimSpace(p.Stanza["source"])
	if s == "" {
		return p.Name
	}
	if i := strings.Index(s, "("); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// Has returns true if the index contains a package with the given name.
func (idx *Index) Has(name string) bool {
	_, ok := idx.packages[name]
	return ok
}

// Providers returns all packages that declare they Provide the given
// virtual package name. Returns nil if no providers exist.
func (idx *Index) Providers(name string) []*Package {
	return idx.providers[name]
}

// EssentialPackages returns all packages with Essential: yes.
func (idx *Index) EssentialPackages() []*Package {
	var ess []*Package
	for _, pkg := range idx.all {
		if pkg.Essential {
			ess = append(ess, pkg)
		}
	}
	return ess
}

// All returns all packages in the index.
func (idx *Index) All() []*Package {
	result := make([]*Package, len(idx.all))
	copy(result, idx.all)
	return result
}

// Len returns the number of packages in the index.
func (idx *Index) Len() int {
	return len(idx.all)
}

// Add inserts a package into the index, replacing any existing entry with the
// same name. Provider mappings are updated.
func (idx *Index) Add(pkg *Package) {
	if _, exists := idx.packages[pkg.Name]; exists {
		for i, p := range idx.all {
			if p.Name == pkg.Name {
				idx.all[i] = pkg
				break
			}
		}
	} else {
		idx.all = append(idx.all, pkg)
	}
	idx.packages[pkg.Name] = pkg
	for _, alt := range pkg.Provides {
		for _, dep := range alt {
			if dep.Name != "" {
				idx.providers[dep.Name] = append(idx.providers[dep.Name], pkg)
			}
		}
	}
}

// Merge combines two indices into a new index. Entries from other override
// entries in idx when they share the same package name. The original indices
// are not modified.
func (idx *Index) Merge(other *Index) *Index {
	merged := &Index{
		packages:  make(map[string]*Package, len(idx.packages)+len(other.packages)),
		providers: make(map[string][]*Package),
	}

	// Copy all packages from idx first.
	for _, pkg := range idx.all {
		merged.packages[pkg.Name] = pkg
		merged.all = append(merged.all, pkg)
	}

	// Then apply other's packages, overriding on name collision.
	for _, pkg := range other.all {
		if _, exists := merged.packages[pkg.Name]; exists {
			// Remove the old entry from merged.all and replace it.
			for i, p := range merged.all {
				if p.Name == pkg.Name {
					merged.all[i] = pkg
					break
				}
			}
		} else {
			merged.all = append(merged.all, pkg)
		}
		merged.packages[pkg.Name] = pkg
	}

	// Rebuild providers index from scratch.
	for _, pkg := range merged.all {
		for _, alt := range pkg.Provides {
			for _, dep := range alt {
				if dep.Name != "" {
					merged.providers[dep.Name] = append(merged.providers[dep.Name], pkg)
				}
			}
		}
	}

	return merged
}

// Sub creates a new Index containing only packages whose names appear in the
// given list. Packages not found in the index are silently skipped.
func (idx *Index) Sub(names []string) *Index {
	sub := New()
	for _, name := range names {
		if pkg := idx.Get(name); pkg != nil {
			sub.Add(pkg)
		}
	}
	return sub
}

// ParsePackageFromStanza creates a Package from a deb822 Stanza, pre-parsing
// all dependency fields.
func ParsePackageFromStanza(stanza deb822.Stanza) (*Package, error) {
	name := stanza["package"]
	if name == "" {
		return nil, fmt.Errorf("stanza missing Package field")
	}

	pkg := &Package{
		Name:         name,
		Version:      stanza["version"],
		Architecture: stanza["architecture"],
		Priority:     strings.TrimSpace(stanza["priority"]),
		Stanza:       stanza,
		Filename:     stanza["filename"],
	}

	// Essential field.
	if strings.EqualFold(strings.TrimSpace(stanza["essential"]), "yes") {
		pkg.Essential = true
	}

	// SHA256: prefer "sha256" field, fallback to "checksums-sha256" if present.
	if sha := stanza["sha256"]; sha != "" {
		pkg.SHA256 = strings.TrimSpace(sha)
	} else if sha := stanza["checksums-sha256"]; sha != "" {
		pkg.SHA256 = strings.TrimSpace(sha)
	}

	// Size.
	if sizeStr := strings.TrimSpace(stanza["size"]); sizeStr != "" {
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err == nil {
			pkg.Size = size
		}
	}

	// Parse dependency fields.
	var err error

	if pkg.Depends, err = parseDeps(stanza["depends"]); err != nil {
		return nil, fmt.Errorf("package %s: parsing Depends: %w", name, err)
	}
	if pkg.PreDepends, err = parseDeps(stanza["pre-depends"]); err != nil {
		return nil, fmt.Errorf("package %s: parsing Pre-Depends: %w", name, err)
	}
	if pkg.Conflicts, err = parseDeps(stanza["conflicts"]); err != nil {
		return nil, fmt.Errorf("package %s: parsing Conflicts: %w", name, err)
	}
	if pkg.Provides, err = parseDeps(stanza["provides"]); err != nil {
		return nil, fmt.Errorf("package %s: parsing Provides: %w", name, err)
	}
	if pkg.Breaks, err = parseDeps(stanza["breaks"]); err != nil {
		return nil, fmt.Errorf("package %s: parsing Breaks: %w", name, err)
	}

	return pkg, nil
}

// parseDeps is a helper that parses a dependency string, treating empty
// strings as no dependencies (not an error).
func parseDeps(s string) (depends.DependencyList, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	return depends.Parse(s)
}
