package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/objstore"
)

// storeControl writes a deb822 control stanza as a blob and returns its hash.
func storeControl(t *testing.T, store *objstore.Store, stanza string) objstore.Hash {
	t.Helper()
	h, err := store.Blobs.Store(strings.NewReader(stanza))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// A binary whose runtime Depends are all in the local build set passes locality.
func TestValidateLocalityAcceptsLocalDeps(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	writeSourcePkg(t, filepath.Join(root, "pkgs", "app"), "Source: app\nPackage: app1\n", "")
	writeSourcePkg(t, filepath.Join(root, "pkgs", "lib"), "Source: lib\nPackage: lib1\n", "")

	app := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "app", PkgDir: filepath.Join(root, "pkgs", "app"), Arch: "amd64", Store: store})
	lib := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "lib", PkgDir: filepath.Join(root, "pkgs", "lib"), Arch: "amd64", Store: store})
	appBin := app.Binary("app1")
	appBin.extraDeps = []*debianBinaryPkg{lib.Binary("lib1")}
	appBin.depsResolved = true

	control := storeControl(t, store, "Package: app1\nVersion: 1\nArchitecture: amd64\nDepends: lib1 (>= 1)\n")
	if err := appBin.validateLocality(store, control); err != nil {
		t.Fatalf("locality should pass when dep is local: %v", err)
	}
}

// A binary with a runtime dep that is neither locally built nor allowed via
// lockfile_deps fails locality.
func TestValidateLocalityRejectsExternalDep(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	writeSourcePkg(t, filepath.Join(root, "pkgs", "app"), "Source: app\nPackage: app1\n", "")

	app := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "app", PkgDir: filepath.Join(root, "pkgs", "app"), Arch: "amd64", Store: store})
	appBin := app.Binary("app1")
	appBin.depsResolved = true // no local deps

	control := storeControl(t, store, "Package: app1\nVersion: 1\nArchitecture: amd64\nDepends: libmystery1\n")
	err := appBin.validateLocality(store, control)
	if err == nil {
		t.Fatal("locality should fail for a non-local dependency")
	}
	if !strings.Contains(err.Error(), "libmystery1") {
		t.Errorf("error should name the unsatisfied dep, got: %v", err)
	}
}

// lockfile_deps tolerate a named external dependency during locality.
func TestValidateLocalityAllowsLockfileDeps(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	pkgDir := filepath.Join(root, "pkgs", "app")
	writeSourcePkg(t, pkgDir, "Source: app\nPackage: app1\n", "")
	os.WriteFile(filepath.Join(pkgDir, "build.yml"), []byte("lockfile_deps:\n  app1: [libgcc-s1]\n"), 0644)

	app := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "app", PkgDir: pkgDir, Arch: "amd64", Store: store})
	appBin := app.Binary("app1")
	appBin.depsResolved = true

	control := storeControl(t, store, "Package: app1\nVersion: 1\nArchitecture: amd64\nDepends: libgcc-s1\n")
	if err := appBin.validateLocality(store, control); err != nil {
		t.Fatalf("locality should pass when the external dep is in lockfile_deps: %v", err)
	}
}

// An alternative (A | B) is satisfied when either side is local.
func TestValidateLocalityAlternatives(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	writeSourcePkg(t, filepath.Join(root, "pkgs", "app"), "Source: app\nPackage: app1\n", "")
	writeSourcePkg(t, filepath.Join(root, "pkgs", "lib"), "Source: lib\nPackage: lib1\n", "")

	app := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "app", PkgDir: filepath.Join(root, "pkgs", "app"), Arch: "amd64", Store: store})
	lib := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "lib", PkgDir: filepath.Join(root, "pkgs", "lib"), Arch: "amd64", Store: store})
	appBin := app.Binary("app1")
	appBin.extraDeps = []*debianBinaryPkg{lib.Binary("lib1")}
	appBin.depsResolved = true

	control := storeControl(t, store, "Package: app1\nVersion: 1\nArchitecture: amd64\nDepends: notlocal | lib1\n")
	if err := appBin.validateLocality(store, control); err != nil {
		t.Fatalf("locality should pass when one alternative is local: %v", err)
	}
}
