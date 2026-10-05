package install

import (
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/resolver"
)

// Resolve computes the closed install set (deps + pre-deps) for the given
// package names. This is the single resolver entry point used by both
// Bootstrap and Install — no dual implementations.
func Resolve(idx *index.Index, arch string, names []string) ([]*index.Package, error) {
	var roots []resolver.Requirement
	for _, name := range names {
		roots = append(roots, resolver.Requirement{Name: name})
	}
	if len(roots) == 0 {
		return nil, nil
	}

	r := resolver.New(idx, arch)
	result, err := r.Resolve(roots)
	if err != nil {
		return nil, err
	}
	return result.Packages, nil
}
