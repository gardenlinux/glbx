package build

import (
	"fmt"
	"strings"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/debian/deb822"
	"github.com/gardenlinux/glbx/internal/debian/depends"
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

// Build validates the selected binary: it checks that every runtime dependency
// is satisfiable from the locally built set (plus any per-binary lockfile_deps
// allowance), runs an install check in a bootstrapped sandbox, and re-emits the
// validated .deb and control as this artifact's outputs.
func (b *debianBinaryPkg) Build(ctx artifact.BuildContext) ([]artifact.Output, error) {
	l := log.From(ctx.Ctx, log.Binary)
	l.Info("validating %s", b.name)

	debHash, ok := ctx.Inputs[b.name+".deb"]
	if !ok {
		return nil, fmt.Errorf("binary package %s: no .deb in resolved inputs", b.name)
	}
	controlKey := "control:" + b.name
	controlHash, ok := ctx.Inputs[controlKey]
	if !ok {
		return nil, fmt.Errorf("binary package %s: control metadata not found", b.name)
	}

	if err := b.validateLocality(ctx.Store, controlHash); err != nil {
		return nil, err
	}
	l.Info("%s: locality check passed", b.name)

	if err := b.installCheck(ctx.Ctx, ctx.Store, debHash, controlHash); err != nil {
		return nil, err
	}

	return []artifact.Output{
		{Name: b.name + ".deb", Hash: debHash},
		{Name: controlKey, Hash: controlHash},
	}, nil
}

// validateLocality parses the binary's control stanza and verifies every
// runtime Depends/Pre-Depends alternative is satisfiable either from the local
// build closure or from this binary's declared lockfile_deps allowance.
func (b *debianBinaryPkg) validateLocality(store *objstore.Store, controlHash objstore.Hash) error {
	reader, err := store.Blobs.Open(controlHash)
	if err != nil {
		return fmt.Errorf("binary package %s: open control blob: %w", b.name, err)
	}
	defer reader.Close()

	control, err := deb822.NewReader(reader).Next()
	if err != nil {
		return fmt.Errorf("binary package %s: parse control: %w", b.name, err)
	}

	var allDeps string
	if d, ok := control["depends"]; ok {
		allDeps = d
	}
	if pd, ok := control["pre-depends"]; ok {
		if allDeps != "" {
			allDeps += ", "
		}
		allDeps += pd
	}
	if allDeps == "" {
		return nil
	}

	depList, err := depends.Parse(allDeps)
	if err != nil {
		return fmt.Errorf("binary package %s: parse depends %q: %w", b.name, allDeps, err)
	}

	localSet := b.buildLocalSet()

	lockfileAllowed := make(map[string]bool)
	for _, name := range b.lockfileDepNames() {
		lockfileAllowed[name] = true
	}

	var unsatisfied []string
	for _, alt := range depList {
		if altSatisfied(alt, localSet) || altSatisfied(alt, lockfileAllowed) {
			continue
		}
		var names []string
		for _, d := range alt {
			names = append(names, d.Name)
		}
		unsatisfied = append(unsatisfied, strings.Join(names, " | "))
	}
	if len(unsatisfied) > 0 {
		return fmt.Errorf("binary package %s: locality check failed — runtime deps not locally built: [%s]",
			b.name, strings.Join(unsatisfied, ", "))
	}
	return nil
}

// buildLocalSet is the set of package names (and their virtual provides)
// reachable from this binary via extraDeps and includes.
func (b *debianBinaryPkg) buildLocalSet() map[string]bool {
	local := make(map[string]bool)
	var walk func(bp *debianBinaryPkg)
	walk = func(bp *debianBinaryPkg) {
		if local[bp.name] {
			return
		}
		local[bp.name] = true
		bp.resolveExtraDeps()
		for _, dep := range bp.extraDeps {
			walk(dep)
		}
		for _, inc := range bp.includes {
			walk(inc)
		}
	}
	walk(b)

	if b.pkgSet != nil {
		provides := b.pkgSet.ProvidesMap()
		for name := range local {
			for _, virt := range provides[name] {
				local[virt] = true
			}
		}
	}
	return local
}

// altSatisfied reports whether at least one alternative name is in set.
func altSatisfied(alt []depends.Dependency, set map[string]bool) bool {
	for _, dep := range alt {
		if set[dep.Name] {
			return true
		}
	}
	return false
}

// lockfileDepNames returns the lockfile_deps declared on this binary. They are
// strictly local to the declaring binary and never inherited by consumers.
func (b *debianBinaryPkg) lockfileDepNames() []string {
	buildYML, err := buildcfg.LoadBuildYML(b.sourceBuild.PkgDir)
	if err != nil {
		return nil
	}
	ldeps, ok := buildYML.LockfileDeps[b.name]
	if !ok {
		return nil
	}
	out := make([]string, len(ldeps))
	copy(out, ldeps)
	return out
}
