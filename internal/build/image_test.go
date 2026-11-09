package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gardenlinux/glbx/internal/buildcfg"
)

// writeImageType lays down an images.d/<type>/ with a conf.yml, a build script,
// and a tooling-deps.yml pinning one arch-independent file at the given hash.
func writeImageType(t *testing.T, confDir, typeName, script, confYML, pinHash string) string {
	t.Helper()
	typeDir := filepath.Join(confDir, "images.d", typeName)
	if err := os.MkdirAll(typeDir, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(typeDir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("conf.yml", confYML, 0644)
	write("build", script, 0755)
	write("tooling-deps.yml", "tools:\n  - name: e2fsprogs\n    version: \"1.0\"\n    files:\n      - arch: all\n        sha256: \""+pinHash+"\"\n        urls: [\"http://example/e2fsprogs.deb\"]\n", 0644)
	return typeDir
}

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// newTestImage builds an Image over a rootfs with one dep and a type directory.
func newTestImage(t *testing.T, confDir, typeDir string, rootfs *Rootfs) *Image {
	t.Helper()
	conf, err := buildcfg.LoadImageTypeConf(typeDir)
	if err != nil {
		t.Fatal(err)
	}
	return newImage(ImageConfig{
		Name:    "base-ext4",
		Arch:    "amd64",
		TypeDir: typeDir,
		Rootfs:  rootfs,
		Conf:    conf,
		BaseDir: confDir,
		Store:   rootfs.store,
	})
}

func TestImageKey(t *testing.T) {
	store := newStore(t)
	confDir := t.TempDir()
	rootfs := newRootfsDirect("base", "amd64", nil, store)
	typeDir := writeImageType(t, confDir, "plain-ext4", "#!/bin/sh\n", "debian_tooling: [e2fsprogs]\n", hashA)
	im := newTestImage(t, confDir, typeDir, rootfs)
	if got, want := im.Key(), "image:base-ext4:amd64"; got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

func TestToolingPkgBuildKey(t *testing.T) {
	store := newStore(t)
	pkgDir := filepath.Join(t.TempDir(), "images.d", "tooling", "e2fsprogs")
	writeSourcePkg(t, pkgDir, "Source: e2fsprogs\n", "")
	tb := NewToolingPkgBuild(ToolingPkgBuildConfig{Name: "e2fsprogs", PkgDir: pkgDir, Arch: "amd64", Store: store})
	if got, want := tb.Key(), "tooling-pkg-build:e2fsprogs:amd64"; got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

func TestImageIdentityStable(t *testing.T) {
	store := newStore(t)
	confDir := t.TempDir()
	rootfs := newRootfsDirect("base", "amd64", nil, store)
	typeDir := writeImageType(t, confDir, "plain-ext4", "#!/bin/sh\n", "debian_tooling: [e2fsprogs]\n", hashA)
	a, err := newTestImage(t, confDir, typeDir, rootfs).Identity()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newTestImage(t, confDir, typeDir, rootfs).Identity()
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equal(b) {
		t.Fatalf("image identity should be deterministic: %s != %s", a, b)
	}
}

func TestImageIdentitySensitivity(t *testing.T) {
	store := newStore(t)
	confDir := t.TempDir()
	rootfs := newRootfsDirect("base", "amd64", nil, store)
	typeDir := writeImageType(t, confDir, "plain-ext4", "#!/bin/sh\n", "debian_tooling: [e2fsprogs]\n", hashA)
	base, err := newTestImage(t, confDir, typeDir, rootfs).Identity()
	if err != nil {
		t.Fatal(err)
	}

	// Changing the assembly script bytes changes the identity.
	if err := os.WriteFile(filepath.Join(typeDir, "build"), []byte("#!/bin/sh\necho changed\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if id, _ := newTestImage(t, confDir, typeDir, rootfs).Identity(); id.Equal(base) {
		t.Error("identity must change when the assembly script changes")
	}

	// Restore script, change conf.yml.
	writeImageType(t, confDir, "plain-ext4", "#!/bin/sh\n", "debian_tooling: [e2fsprogs, fdisk]\n", hashA)
	if id, _ := newTestImage(t, confDir, typeDir, rootfs).Identity(); id.Equal(base) {
		t.Error("identity must change when conf.yml changes")
	}

	// Restore conf, change a pinned tooling file hash.
	writeImageType(t, confDir, "plain-ext4", "#!/bin/sh\n", "debian_tooling: [e2fsprogs]\n", hashB)
	if id, _ := newTestImage(t, confDir, typeDir, rootfs).Identity(); id.Equal(base) {
		t.Error("identity must change when a pinned tooling file hash changes")
	}
}

// A tooling build and a shipped source build over a byte-identical tree never
// collide on key or identity — the tooling seed and key namespace keep them
// apart, which is the structural-separation guarantee.
func TestToolingShippedNoCollision(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	shippedDir := filepath.Join(root, "pkgs", "e2fsprogs")
	toolingDir := filepath.Join(root, "images.d", "tooling", "e2fsprogs")
	control := "Source: e2fsprogs\nPackage: e2fsprogs\nArchitecture: any\n"
	writeSourcePkg(t, shippedDir, control, "")
	writeSourcePkg(t, toolingDir, control, "")

	shipped := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "e2fsprogs", PkgDir: shippedDir, Arch: "amd64", Store: store})
	tooling := NewToolingPkgBuild(ToolingPkgBuildConfig{Name: "e2fsprogs", PkgDir: toolingDir, Arch: "amd64", Store: store})

	if shipped.Key() == tooling.Key() {
		t.Fatalf("keys must differ: %s", shipped.Key())
	}
	sid, _ := shipped.Identity()
	tid, _ := tooling.Identity()
	if sid.Equal(tid) {
		t.Fatalf("identities must differ even over an identical tree: %s", sid)
	}
}
