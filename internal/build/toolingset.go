package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// ToolingSet is the registry of image-assembly tooling builds, scanned from
// images.d/tooling/. It mirrors PackageSet but for the tooling universe: it
// produces only ToolingPkgBuild nodes and resolves references only among them,
// so a tooling build_depends can never reach a pkgs/ binary and a shipped
// build_depends can never reach a tooling build — the separation is structural.
type ToolingSet struct {
	toolingDir string
	arch       string
	store      *objstore.Store
	stubPath   string
	scratchDir string
	builds     map[string]*ToolingPkgBuild
	depsWired  bool
}

// NewToolingSet scans images.d/tooling/ and registers one ToolingPkgBuild per
// source package found (a directory with src/debian/control). A missing
// tooling directory yields an empty, usable set — a config repo may have an
// image layer with no local tooling, or no image layer at all.
func NewToolingSet(toolingDir, arch string, store *objstore.Store, stubPath, scratchDir string) (*ToolingSet, error) {
	ts := &ToolingSet{
		toolingDir: toolingDir,
		arch:       arch,
		store:      store,
		stubPath:   stubPath,
		scratchDir: scratchDir,
		builds:     make(map[string]*ToolingPkgBuild),
	}

	entries, err := os.ReadDir(toolingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return ts, nil
		}
		return nil, fmt.Errorf("read tooling directory %s: %w", toolingDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(toolingDir, name)
		if _, err := os.Stat(filepath.Join(dir, "src", "debian", "control")); err != nil {
			continue
		}
		buildYML, err := buildcfg.LoadBuildYML(dir)
		if err != nil {
			return nil, fmt.Errorf("tooling %s: %w", name, err)
		}
		if err := buildcfg.ValidateToolingBuildYML(buildYML); err != nil {
			return nil, fmt.Errorf("tooling %s: %w", name, err)
		}
		ts.builds[name] = NewToolingPkgBuild(ToolingPkgBuildConfig{
			Name:       name,
			PkgDir:     dir,
			Arch:       arch,
			Store:      store,
			StubPath:   stubPath,
			ScratchDir: scratchDir,
		})
	}

	if err := ts.wireDeps(); err != nil {
		return nil, err
	}
	return ts, nil
}

// wireDeps resolves each tooling build's build_depends to other tooling builds
// within this set and records them. A build_depends naming a source absent from
// the tooling universe is an error — tooling edges must stay inside tooling.
func (ts *ToolingSet) wireDeps() error {
	if ts.depsWired {
		return nil
	}
	ts.depsWired = true
	for name, build := range ts.builds {
		buildYML, err := buildcfg.LoadBuildYML(build.PkgDir)
		if err != nil {
			return fmt.Errorf("tooling %s: %w", name, err)
		}
		var deps []*ToolingPkgBuild
		seen := make(map[string]bool)
		for _, spec := range buildYML.BuildDepends {
			src, _, ok := strings.Cut(spec, ":")
			if !ok || src == "" {
				return fmt.Errorf("tooling %s: build_depends %q must be <source>:<binary>", name, spec)
			}
			dep, ok := ts.builds[src]
			if !ok {
				return fmt.Errorf("tooling %s: build_depends %q names source %q absent from images.d/tooling/", name, spec, src)
			}
			if !seen[src] {
				seen[src] = true
				deps = append(deps, dep)
			}
		}
		build.SetBuildDeps(deps)
	}
	return nil
}

// Build returns the tooling build for a source name, or nil if absent.
func (ts *ToolingSet) Build(src string) *ToolingPkgBuild {
	return ts.builds[src]
}

// ToolingBinary resolves a "<source>:<binary>" local_tooling reference to the
// tooling build node that produces it and the output name of the named binary's
// .deb. It errors if the source is not in the tooling universe or the source
// declares no such binary.
func (ts *ToolingSet) ToolingBinary(src, bin string) (*ToolingPkgBuild, string, error) {
	build, ok := ts.builds[src]
	if !ok {
		return nil, "", fmt.Errorf("tooling source %q not found in images.d/tooling/", src)
	}
	for _, name := range ParseBinaryPackageNames(build.PkgDir) {
		if name == bin {
			return build, bin + ".deb", nil
		}
	}
	return nil, "", fmt.Errorf("tooling source %q produces no binary %q", src, bin)
}

// All returns every tooling build in the set.
func (ts *ToolingSet) All() []*ToolingPkgBuild {
	out := make([]*ToolingPkgBuild, 0, len(ts.builds))
	for _, b := range ts.builds {
		out = append(out, b)
	}
	return out
}
