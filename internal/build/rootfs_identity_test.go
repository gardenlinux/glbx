package build

import (
	"os"
	"path/filepath"
	"testing"
)

// The rootfs identity folds every transitive binary dependency, so a change to
// any package in the closure changes the image identity.
func TestRootfsIdentityChangesWhenTransitiveDepChanges(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()

	libDir := filepath.Join(root, "pkgs", "lib")
	writeSourcePkg(t, libDir, "Source: lib\nPackage: lib1\nArchitecture: any\n", "")
	appDir := filepath.Join(root, "pkgs", "app")
	writeSourcePkg(t, appDir, "Source: app\nPackage: app-bin\nArchitecture: any\n", "")
	os.WriteFile(filepath.Join(appDir, "build.yml"), []byte("build_depends: [\"lib:lib1\"]\n"), 0644)

	identity := func() string {
		ps, err := NewPackageSet(filepath.Join(root, "pkgs"), "amd64", store, "")
		if err != nil {
			t.Fatal(err)
		}
		bp, err := ps.Binary("app", "app-bin")
		if err != nil {
			t.Fatal(err)
		}
		rootfs := newRootfsDirect("img", "amd64", []*debianBinaryPkg{bp}, store)
		id, err := rootfs.Identity()
		if err != nil {
			t.Fatal(err)
		}
		return id.String()
	}

	id1 := identity()
	// Change the transitive dependency's source.
	writeSourcePkg(t, libDir, "Source: lib\nPackage: lib1\nArchitecture: any\nDescription: changed\n", "")
	id2 := identity()

	if id1 == id2 {
		t.Fatalf("rootfs identity must change when a transitive dep's source changes: %s == %s", id1, id2)
	}
}

// The rootfs identity is deterministic for unchanged inputs.
func TestRootfsIdentityStable(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	writeSourcePkg(t, filepath.Join(root, "pkgs", "app"), "Source: app\nPackage: app-bin\nArchitecture: any\n", "")

	identity := func() string {
		ps, _ := NewPackageSet(filepath.Join(root, "pkgs"), "amd64", store, "")
		bp, _ := ps.Binary("app", "app-bin")
		id, err := newRootfsDirect("img", "amd64", []*debianBinaryPkg{bp}, store).Identity()
		if err != nil {
			t.Fatal(err)
		}
		return id.String()
	}
	if identity() != identity() {
		t.Fatal("rootfs identity should be deterministic for unchanged inputs")
	}
}

// NewPackageSet discovers source builds and resolves src:binary references.
func TestPackageSetDiscovery(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	writeSourcePkg(t, filepath.Join(root, "pkgs", "foo"), "Source: foo\nPackage: foo1\nProvides: virtfoo\n", "")

	ps, err := NewPackageSet(filepath.Join(root, "pkgs"), "amd64", store, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ps.Binary("foo", "foo1"); err != nil {
		t.Fatalf("Binary(foo,foo1): %v", err)
	}
	if _, err := ps.Binary("nope", "x"); err == nil {
		t.Fatal("expected error for unknown source")
	}
	if !ps.LocalSet()["foo1"] || !ps.LocalSet()["virtfoo"] {
		t.Errorf("LocalSet missing foo1/virtfoo: %v", ps.LocalSet())
	}
	if provides := ps.ProvidesMap()["foo1"]; len(provides) != 1 || provides[0] != "virtfoo" {
		t.Errorf("ProvidesMap[foo1] = %v, want [virtfoo]", provides)
	}
}
