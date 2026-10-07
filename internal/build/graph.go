package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// PackageSet is the shared registry of all source builds and binary packages.
// Artifacts use it to resolve src:pkg references to concrete artifact instances.
type PackageSet struct {
	pkgsDir      string
	arch         string
	store        *objstore.Store
	stubPath     string
	sourceBuilds map[string]*DebianPkgBuild
	localSet     map[string]bool
	providesMap  map[string][]string // binary pkg name → virtual packages it provides
}

// NewPackageSet scans the packages directory and creates the registry.
func NewPackageSet(pkgsDir, arch string, store *objstore.Store, stubPath string) (*PackageSet, error) {
	ps := &PackageSet{
		pkgsDir:      pkgsDir,
		arch:         arch,
		store:        store,
		stubPath:     stubPath,
		sourceBuilds: make(map[string]*DebianPkgBuild),
		localSet:     make(map[string]bool),
		providesMap:  make(map[string][]string),
	}

	entries, err := os.ReadDir(pkgsDir)
	if err != nil {
		return nil, fmt.Errorf("read pkgs directory %s: %w", pkgsDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(pkgsDir, name)

		if _, err := os.Stat(filepath.Join(dir, "src", "debian", "control")); err != nil {
			continue
		}

		sb := NewDebianPkgBuild(DebianPkgBuildConfig{
			Name:     name,
			PkgDir:   dir,
			Arch:     arch,
			Store:    store,
			StubPath: stubPath,
		})
		sb.pkgSet = ps
		ps.sourceBuilds[name] = sb

		binNames := ParseBinaryPackageNames(dir)
		for _, binName := range binNames {
			ps.localSet[binName] = true
		}
		virtuals := ParseBinaryPackageProvides(dir)
		for _, virt := range virtuals {
			ps.localSet[virt] = true
		}
		// Map each binary package to its own virtual provides
		perBinProvides := ParseBinaryPackageProvidesMap(dir)
		for binName, virts := range perBinProvides {
			ps.providesMap[binName] = append(ps.providesMap[binName], virts...)
		}
	}

	return ps, nil
}

// Binary returns the debianBinaryPkg for the given src:pkg reference.
func (ps *PackageSet) Binary(src, pkg string) (*debianBinaryPkg, error) {
	sb, ok := ps.sourceBuilds[src]
	if !ok {
		return nil, fmt.Errorf("source package %q not found", src)
	}
	bp := sb.Binary(pkg)
	bp.pkgSet = ps
	return bp, nil
}

// LocalSet returns the set of all binary package names across all sources.
func (ps *PackageSet) LocalSet() map[string]bool {
	return ps.localSet
}

// ProvidesMap returns the mapping of binary package names to their virtual
// packages (from Provides: fields in debian/control).
func (ps *PackageSet) ProvidesMap() map[string][]string {
	return ps.providesMap
}

// BinaryByName looks up a binary package by its name across all source builds.
func (ps *PackageSet) BinaryByName(name string) (*debianBinaryPkg, error) {
	for srcName, sb := range ps.sourceBuilds {
		for _, binName := range ParseBinaryPackageNames(sb.PkgDir) {
			if binName == name {
				return ps.Binary(srcName, name)
			}
		}
	}
	return nil, fmt.Errorf("binary package %q not found in any source", name)
}

// GraphConfig holds parameters for constructing a complete artifact graph.
type GraphConfig struct {
	ConfDir  string
	Arch     string
	Store    *objstore.Store
	StubPath string
}

// GraphResult holds the constructed artifact graph and its components.
type GraphResult struct {
	Graph  *artifact.Graph
	Rootfs *Rootfs
}

// BuildGraph constructs the full artifact graph. It creates a PackageSet,
// creates the Rootfs artifact (which reads rootfs.yml), and lets Discover()
// walk the dependency chain to build the graph.
func BuildGraph(cfg GraphConfig) (*GraphResult, error) {
	pkgsDir := filepath.Join(cfg.ConfDir, "pkgs")
	ps, err := NewPackageSet(pkgsDir, cfg.Arch, cfg.Store, cfg.StubPath)
	if err != nil {
		return nil, err
	}

	if len(ps.sourceBuilds) == 0 {
		return nil, fmt.Errorf("no packages found in %s", pkgsDir)
	}

	rootfs, err := NewRootfs(RootfsConfig{
		Arch:     cfg.Arch,
		Store:    cfg.Store,
		PkgSet:   ps,
		BaseDir:  cfg.ConfDir,
		StubPath: cfg.StubPath,
	})
	if err != nil {
		return nil, fmt.Errorf("create rootfs artifact: %w", err)
	}

	graph, err := artifact.Discover([]artifact.Artifact{rootfs})
	if err != nil {
		return nil, fmt.Errorf("discover graph: %w", err)
	}

	return &GraphResult{
		Graph:  graph,
		Rootfs: rootfs,
	}, nil
}

// BuiltOutputs walks the graph and, for each node that has already been built,
// collects the blobs its result occupies and the identity→manifest alias that
// records it. A node not yet built contributes nothing. This is the single
// enumeration both garbage collection (which keeps the blobs) and publishing
// (which mirrors the blobs and sets the aliases on a remote) consume.
func (gr *GraphResult) BuiltOutputs(store *objstore.Store) (blobs []objstore.Hash, aliases []objstore.MapAlias) {
	for _, key := range gr.Graph.Keys() {
		a := gr.Graph.Find(key)
		manifest, leaves, err := a.OutputRefs(store)
		if err != nil {
			continue // not built — contributes nothing
		}
		identity, err := a.Identity()
		if err != nil {
			continue
		}
		blobs = append(blobs, manifest)
		blobs = append(blobs, leaves...)
		aliases = append(aliases, objstore.MapAlias{Identity: identity, ManifestHash: manifest})
	}
	return blobs, aliases
}

func parseSrcPkg(spec string) (src, pkg string, err error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%q must be in src:pkg format", spec)
	}
	return parts[0], parts[1], nil
}
