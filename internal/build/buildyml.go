package build

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gardenlinux/glbx/internal/debian/deb822"
)

func readControl(pkgDir string) []deb822.Stanza {
	data, err := os.ReadFile(filepath.Join(pkgDir, "src", "debian", "control"))
	if err != nil {
		return nil
	}
	r := deb822.NewReader(bytes.NewReader(data))
	var stanzas []deb822.Stanza
	for {
		st, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stanzas
		}
		stanzas = append(stanzas, st)
	}
	return stanzas
}

// splitProvides parses a Provides: field value into individual virtual package
// names, stripping any version constraints like "awk (= 1:1.3.4-7)".
func splitProvides(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if idx := strings.IndexByte(item, '('); idx > 0 {
			item = strings.TrimSpace(item[:idx])
		}
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// ParseBinaryPackageNames reads debian/control and returns all binary package
// names declared in Package: stanzas.
func ParseBinaryPackageNames(pkgDir string) []string {
	var names []string
	for _, st := range readControl(pkgDir) {
		if name := strings.TrimSpace(st["package"]); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// ParseBinaryPackageProvides reads debian/control and returns all virtual
// package names from Provides: fields across all binary stanzas.
func ParseBinaryPackageProvides(pkgDir string) []string {
	var provides []string
	for _, st := range readControl(pkgDir) {
		if v := st["provides"]; v != "" {
			provides = append(provides, splitProvides(v)...)
		}
	}
	return provides
}

// ParseBinaryPackageProvidesMap reads debian/control and returns a mapping
// from binary package name to its virtual package provides.
func ParseBinaryPackageProvidesMap(pkgDir string) map[string][]string {
	result := make(map[string][]string)
	for _, st := range readControl(pkgDir) {
		name := strings.TrimSpace(st["package"])
		if name == "" {
			continue
		}
		if v := st["provides"]; v != "" {
			result[name] = append(result[name], splitProvides(v)...)
		}
	}
	return result
}
