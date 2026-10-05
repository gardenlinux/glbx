package build

import (
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// walkBinaries returns the transitive closure of binary packages reachable from
// roots via extraDeps and includes. Each binary is yielded once, in first-seen
// (DFS) order. Each visited package's extraDeps are resolved on the fly.
func walkBinaries(roots []*debianBinaryPkg) []*debianBinaryPkg {
	seen := make(map[string]bool)
	var result []*debianBinaryPkg
	var walk func(bp *debianBinaryPkg)
	walk = func(bp *debianBinaryPkg) {
		key := bp.sourceBuild.Name + ":" + bp.name
		if seen[key] {
			return
		}
		seen[key] = true
		bp.resolveExtraDeps()
		result = append(result, bp)
		for _, dep := range bp.extraDeps {
			walk(dep)
		}
		for _, inc := range bp.includes {
			walk(inc)
		}
	}
	for _, bp := range roots {
		walk(bp)
	}
	return result
}

// makeLocalIndex builds a package index from the source-build manifests of every
// binary reachable from roots (via walkBinaries). Used wherever an installability
// check or runtime-closure resolution needs a local-only view of the build closure.
func makeLocalIndex(roots []*debianBinaryPkg, store *objstore.Store) *index.Index {
	idx := index.New()
	for _, bp := range walkBinaries(roots) {
		pkg := bp.sourceBuild.LoadBinaryPkg(bp.name, store)
		if pkg != nil && idx.Get(pkg.Name) == nil {
			idx.Add(pkg)
		}
	}
	return idx
}
