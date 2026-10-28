package provider

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gardenlinux/glbx/internal/buildcfg"
)

func init() {
	RegisterLockGenerator(pkgForward{})
}

// pkgForward is the recommended developer entry point for lockfile generation.
// It reads pkg_type from pkgs/<name>/build.yml and delegates to the concrete
// lock generator of that namespace, passing params through untouched, so a
// developer runs `glbx lockfile pkg:<name>` without needing to know whether a
// package is plain Debian or the kernel special case.
type pkgForward struct{}

func (pkgForward) Namespace() string { return "pkg" }

// pkgDelegates is the fixed, ordered set of concrete generators pkg_type may
// select. The order is the ParamsHelp presentation order: debian before
// debian-kernel, so the inherited base reads before the kernel's additions.
var pkgDelegates = []LockGenerator{
	debianLock{},
	debianKernelLock{},
}

// ParamsHelp composes the delegates' help, each under a pkg_type heading, in
// pkgDelegates order, prefixed by how dispatch is selected.
func (pkgForward) ParamsHelp() string {
	var b strings.Builder
	b.WriteString("lockfile generation is dispatched by the required pkg_type field in the package's build.yml. based on it the following parameters are available:\n")
	for _, g := range pkgDelegates {
		fmt.Fprintf(&b, "\npkg_type: %s\n\n%s\n", g.Namespace(), g.ParamsHelp())
	}
	return b.String()
}

func (pkgForward) Generate(op OpContext, arg string) error {
	pkgDir := filepath.Join(op.OutputDir, "pkgs", arg)
	build, err := buildcfg.LoadBuildYML(pkgDir)
	if err != nil {
		return err
	}
	if build.PkgType == "" {
		return fmt.Errorf("pkg %q: build.yml must set pkg_type to use the pkg: lockfile generator", arg)
	}
	g, err := LookupLockGenerator(build.PkgType)
	if err != nil {
		return fmt.Errorf("pkg %q: %w", arg, err)
	}
	if g.Namespace() == "pkg" {
		return fmt.Errorf("pkg_type must name a concrete generator, not \"pkg\"")
	}
	return g.Generate(op, arg)
}
