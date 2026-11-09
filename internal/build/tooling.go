package build

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/dirhash"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// toolingSeed is the per-type identity seed for tooling package builds, folded
// in after the global seed. Its distinctness from sourceSeed guarantees a
// tooling build and a same-named shipped source build never collide on identity
// even over a byte-identical tree. Regenerate when a change makes tooling-build
// derivation incompatible with prior cache entries:
//
//	openssl rand -hex 16
const toolingSeed = "a3f1c9e2b7d04856af2e1c3b9d6e4f70"

// ToolingPkgBuild is an image-assembly tool package's source build. It is its
// own artifact universe: a distinct key namespace, an identity that can never
// alias a shipped source build, build dependencies resolved only against other
// tooling, and no binary-validation layer downstream. Its outputs — the built
// .debs — are consumed directly at image-assembly time.
//
// The source-build mechanics are shared with a shipped package: a configured
// DebianPkgBuild carries the chroot assembly, changelog stamp, and output
// collection, and ToolingPkgBuild delegates the build body to it while
// overriding the artifact-interface edges that make tooling a separate world.
type ToolingPkgBuild struct {
	Name     string
	PkgDir   string
	Arch     string
	store    *objstore.Store
	inner    *DebianPkgBuild
	identity objstore.Hash

	// buildDeps are other tooling builds this package needs in its compile
	// chroot. Resolution is physically confined to the tooling universe by the
	// ToolingSet that populates this slice — it can never reach a pkgs/ binary.
	buildDeps []*ToolingPkgBuild
}

// ToolingPkgBuildConfig holds the parameters for a tooling package build.
type ToolingPkgBuildConfig struct {
	Name       string
	PkgDir     string
	Arch       string
	Store      *objstore.Store
	StubPath   string
	ScratchDir string
}

// NewToolingPkgBuild constructs a tooling build. The embedded DebianPkgBuild is
// given no package set: tooling build dependencies are resolved by the
// ToolingSet and threaded in through SetBuildDeps, never through the shipped
// resolver.
func NewToolingPkgBuild(cfg ToolingPkgBuildConfig) *ToolingPkgBuild {
	inner := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:       cfg.Name,
		PkgDir:     cfg.PkgDir,
		Arch:       cfg.Arch,
		Store:      cfg.Store,
		StubPath:   cfg.StubPath,
		ScratchDir: cfg.ScratchDir,
	})
	return &ToolingPkgBuild{
		Name:   cfg.Name,
		PkgDir: inner.PkgDir,
		Arch:   cfg.Arch,
		store:  cfg.Store,
		inner:  inner,
	}
}

// SetBuildDeps records the tooling builds resolved from this package's
// build_depends. The embedded source build is told about them too so its chroot
// is populated with their outputs.
func (t *ToolingPkgBuild) SetBuildDeps(deps []*ToolingPkgBuild) {
	t.buildDeps = deps
	// The inner build resolves its own deps through pkgSet, which is nil for
	// tooling; mark that resolution done so it never tries the shipped path.
	t.inner.depsResolved = true
}

func (t *ToolingPkgBuild) Key() string {
	return fmt.Sprintf("tooling-pkg-build:%s:%s", t.Name, t.Arch)
}

func (t *ToolingPkgBuild) ShortName() string { return t.Name }

// Depends are the other tooling builds needed at compile time. Edges land only
// on tooling builds — never a shipped binary — so the tooling universe's build
// graph is closed.
func (t *ToolingPkgBuild) Depends() []artifact.Artifact {
	deps := make([]artifact.Artifact, len(t.buildDeps))
	for i, d := range t.buildDeps {
		deps[i] = d
	}
	return deps
}

// Includes returns no closure-only edges — like a shipped source build.
func (t *ToolingPkgBuild) Includes() []artifact.Artifact { return nil }

// Inputs references each build-dep tooling build's .deb outputs, resolved from
// their manifests at build time.
func (t *ToolingPkgBuild) Inputs() []artifact.Input {
	var inputs []artifact.Input
	for _, d := range t.buildDeps {
		for _, bin := range ParseBinaryPackageNames(d.PkgDir) {
			inputs = append(inputs, artifact.Input{Source: d, Name: bin + ".deb"})
		}
	}
	return inputs
}

func (t *ToolingPkgBuild) OutputRefs(store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	return artifact.ResolveOutputRefs(t, store)
}

// Identity folds the tooling seed, the package tree hash, the architecture, and
// each build-dep tooling build's identity. The tooling seed keeps it disjoint
// from any shipped source build's identity.
func (t *ToolingPkgBuild) Identity() (objstore.Hash, error) {
	if !t.identity.IsZero() {
		return t.identity, nil
	}
	dirHash, err := dirhash.HashDirectory(t.PkgDir)
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("hash tooling package directory: %w", err)
	}
	parts := []string{artifact.IDSeed(), toolingSeed, dirHash, t.Arch}
	for _, d := range t.buildDeps {
		id, err := d.Identity()
		if err != nil {
			return objstore.Hash{}, fmt.Errorf("get tooling dep identity: %w", err)
		}
		parts = append(parts, id.String())
	}
	hash := objstore.ConcatHash(parts...)
	t.identity = hash
	return hash, nil
}

// Build compiles the tooling source package. It reuses the shipped source
// build's phases verbatim: the chroot is the pinned build tooling plus every
// build-dep tooling package's .debs (local builds override same-named pins),
// the source is stamped and compiled, and the produced .debs are collected as
// outputs. There is no binary-validation layer.
func (t *ToolingPkgBuild) Build(ctx artifact.BuildContext) ([]artifact.Output, error) {
	store := ctx.Store
	l := log.From(ctx.Ctx, log.Build)
	l.Info("building tooling package: %s", t.Name)

	lockfileIndex, err := t.inner.loadLockfileIndex()
	if err != nil {
		return nil, fmt.Errorf("load build-deps: %w", err)
	}

	// Build-dep tooling .debs come in through ctx.Inputs keyed "<bin>.deb". They
	// override same-named pinned tooling so a local tool cannot be shadowed by
	// the mirror copy.
	localPkgs := t.toolingDepIndex(ctx.Inputs, store).All()
	localNames := make(map[string]bool, len(localPkgs))
	for _, pkg := range localPkgs {
		localNames[pkg.Name] = true
	}
	var chrootPkgs []*index.Package
	for _, pkg := range lockfileIndex.All() {
		if !localNames[pkg.Name] {
			chrootPkgs = append(chrootPkgs, pkg)
		}
	}
	chrootPkgs = append(chrootPkgs, localPkgs...)
	l.Info("%d chroot packages (%d locally built tooling)", len(chrootPkgs), len(localPkgs))

	refs, err := buildcfg.LoadSourcesYML(t.PkgDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load sources.yml: %w", err)
	}

	cont, cleanup, err := t.inner.setupBuildEnv(ctx.Ctx, store, chrootPkgs, refs)
	if err != nil {
		return nil, fmt.Errorf("setup build env: %w", err)
	}
	defer cleanup()

	if err := t.inner.runBuildInContainer(ctx.Ctx, cont, chrootPkgs); err != nil {
		return nil, fmt.Errorf("run build: %w", err)
	}

	outputs, err := t.inner.collectOutputs(ctx.Ctx, cont, store)
	if err != nil {
		return nil, fmt.Errorf("collect outputs: %w", err)
	}
	return outputs, nil
}

// toolingDepIndex builds a package index from the build-dep tooling .debs
// resolved in ctx.Inputs, reading each control stanza from its blob so the
// chroot's local repo carries real metadata.
func (t *ToolingPkgBuild) toolingDepIndex(inputs map[string]objstore.Hash, store *objstore.Store) *index.Index {
	idx := index.New()
	for name, hash := range inputs {
		if filepath.Ext(name) != ".deb" {
			continue
		}
		blobPath, err := store.Blobs.Path(hash)
		if err != nil {
			continue
		}
		control, err := debControl(blobPath)
		if err != nil {
			continue
		}
		control["sha256"] = hash.String()
		pkg, err := index.ParsePackageFromStanza(control)
		if err != nil {
			continue
		}
		if idx.Get(pkg.Name) == nil {
			idx.Add(pkg)
		}
	}
	return idx
}
