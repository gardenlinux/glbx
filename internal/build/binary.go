package build

import (
	"fmt"
	"strings"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// debianBinaryPkg is the per-binary artifact. It performs no compilation: it
// selects one named binary from its parent source build's outputs and re-emits
// it as the thing downstream artifacts depend on. The validation that every
// runtime dependency is locally built, and the install check, are layered on in
// a later stage. Created via DebianPkgBuild.Binary(name).
type debianBinaryPkg struct {
	name         string
	sourceBuild  *DebianPkgBuild
	extraDeps    []*debianBinaryPkg
	includes     []*debianBinaryPkg
	store        *objstore.Store
	identity     objstore.Hash
	pkgSet       binaryResolver
	depsResolved bool
}

func (b *debianBinaryPkg) Key() string {
	return fmt.Sprintf("debian-binary-pkg:%s:%s:%s", b.sourceBuild.Name, b.name, b.sourceBuild.Arch)
}

func (b *debianBinaryPkg) String() string {
	return fmt.Sprintf("binary-pkg:%s", b.name)
}

func (b *debianBinaryPkg) Depends() []artifact.Artifact {
	b.resolveExtraDeps()
	deps := []artifact.Artifact{b.sourceBuild}
	for _, extra := range b.extraDeps {
		deps = append(deps, extra)
	}
	return deps
}

// Includes returns sibling binaries from the same source build referenced via
// runtime_depends. They contribute to the closure but impose no build order:
// siblings are co-produced by the shared source build (a Depends), so an
// explicit edge would only create an artificial cycle.
func (b *debianBinaryPkg) Includes() []artifact.Artifact {
	b.resolveExtraDeps()
	out := make([]artifact.Artifact, 0, len(b.includes))
	for _, inc := range b.includes {
		out = append(out, inc)
	}
	return out
}

func (b *debianBinaryPkg) resolveExtraDeps() {
	if b.depsResolved || b.pkgSet == nil {
		return
	}
	b.depsResolved = true

	buildYML, err := buildcfg.LoadBuildYML(b.sourceBuild.PkgDir)
	if err != nil {
		return
	}

	addDep := func(srcName, binName string) {
		bp, err := b.pkgSet.Binary(srcName, binName)
		if err != nil {
			return
		}
		if bp.sourceBuild == b.sourceBuild {
			b.includes = append(b.includes, bp)
		} else {
			b.extraDeps = append(b.extraDeps, bp)
		}
	}

	for _, depSpec := range buildYML.RuntimeDepends[b.name] {
		if src, bin, ok := strings.Cut(depSpec, ":"); ok {
			addDep(src, bin)
		}
	}
	// build_depends are locally built too, so they count toward locality.
	for _, depSpec := range buildYML.BuildDepends {
		if src, bin, ok := strings.Cut(depSpec, ":"); ok {
			addDep(src, bin)
		}
	}
}

// Inputs references the .deb and control for this binary plus those of any
// sibling Includes, all sourced from the parent source build's manifest.
func (b *debianBinaryPkg) Inputs() []artifact.Input {
	b.resolveExtraDeps()
	inputs := []artifact.Input{
		{Source: b.sourceBuild, Name: b.name + ".deb"},
		{Source: b.sourceBuild, Name: "control:" + b.name},
	}
	for _, sib := range b.includes {
		inputs = append(inputs,
			artifact.Input{Source: b.sourceBuild, Name: sib.name + ".deb"},
			artifact.Input{Source: b.sourceBuild, Name: "control:" + sib.name},
		)
	}
	return inputs
}

func (b *debianBinaryPkg) OutputRefs(store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	return artifact.ResolveOutputRefs(b, store)
}

func (b *debianBinaryPkg) Identity() (objstore.Hash, error) {
	if !b.identity.IsZero() {
		return b.identity, nil
	}
	b.resolveExtraDeps()

	parts := []string{identityScheme, "binary-pkg", b.name}

	srcID, err := b.sourceBuild.Identity()
	if err != nil {
		return objstore.Hash{}, err
	}
	parts = append(parts, srcID.String())

	for _, dep := range b.extraDeps {
		depID, err := dep.Identity()
		if err != nil {
			return objstore.Hash{}, fmt.Errorf("get dep identity for %s: %w", dep.name, err)
		}
		parts = append(parts, depID.String())
	}

	// Sibling includes fold their Key() rather than Identity() to avoid mutual
	// recursion; same-source content is already captured through srcID.
	for _, sib := range b.includes {
		parts = append(parts, "include:"+sib.Key())
	}

	hash := objstore.ConcatHash(parts...)
	b.identity = hash
	return hash, nil
}

// Build re-emits the selected binary's .deb and control from the resolved
// inputs as this artifact's outputs.
func (b *debianBinaryPkg) Build(ctx artifact.BuildContext) ([]artifact.Output, error) {
	l := log.From(ctx.Ctx, log.Binary)
	l.Info("binary package %s", b.name)

	debHash, ok := ctx.Inputs[b.name+".deb"]
	if !ok {
		return nil, fmt.Errorf("binary package %s: no .deb in resolved inputs", b.name)
	}
	controlKey := "control:" + b.name
	controlHash, ok := ctx.Inputs[controlKey]
	if !ok {
		return nil, fmt.Errorf("binary package %s: control metadata not found", b.name)
	}

	return []artifact.Output{
		{Name: b.name + ".deb", Hash: debHash},
		{Name: controlKey, Hash: controlHash},
	}, nil
}
