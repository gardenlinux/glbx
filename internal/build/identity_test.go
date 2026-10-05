package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/objstore"
)

func writeSourcePkg(t *testing.T, pkgDir, control, sourcesYML string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"), []byte(control), 0644); err != nil {
		t.Fatal(err)
	}
	if sourcesYML != "" {
		if err := os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte(sourcesYML), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func newStore(t *testing.T) *objstore.Store {
	t.Helper()
	s, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A source build's identity must change when a pinned orig tarball hash in
// sources.yml changes — the committed sources.yml is part of the dirhash.
func TestSourceBuildIdentityDependsOnSourcesYML(t *testing.T) {
	store := newStore(t)
	pkgDir := filepath.Join(t.TempDir(), "pkgs", "testpkg")
	writeSourcePkg(t, pkgDir, "Source: testpkg\n", `sources:
  - file: "testpkg_1.0.orig.tar.xz"
    sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
`)
	sb1 := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "testpkg", PkgDir: pkgDir, Arch: "amd64", Store: store})
	id1, err := sb1.Identity()
	if err != nil {
		t.Fatal(err)
	}

	writeSourcePkg(t, pkgDir, "Source: testpkg\n", `sources:
  - file: "testpkg_1.0.orig.tar.xz"
    sha256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
`)
	sb2 := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "testpkg", PkgDir: pkgDir, Arch: "amd64", Store: store})
	id2, err := sb2.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if id1.Equal(id2) {
		t.Fatalf("identity must change when the orig tarball hash changes: %s == %s", id1, id2)
	}
}

// A source build's identity changes with the target architecture.
func TestSourceBuildIdentityDependsOnArch(t *testing.T) {
	store := newStore(t)
	pkgDir := filepath.Join(t.TempDir(), "pkgs", "p")
	writeSourcePkg(t, pkgDir, "Source: p\n", "")

	a := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "p", PkgDir: pkgDir, Arch: "amd64", Store: store})
	b := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "p", PkgDir: pkgDir, Arch: "arm64", Store: store})
	ida, _ := a.Identity()
	idb, _ := b.Identity()
	if ida.Equal(idb) {
		t.Fatalf("identity must differ across architectures")
	}
}

// A source build's identity is deterministic for identical inputs.
func TestSourceBuildIdentityStable(t *testing.T) {
	store := newStore(t)
	pkgDir := filepath.Join(t.TempDir(), "pkgs", "p")
	writeSourcePkg(t, pkgDir, "Source: p\nPackage: p1\n", "")

	a, _ := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "p", PkgDir: pkgDir, Arch: "amd64", Store: store}).Identity()
	b, _ := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "p", PkgDir: pkgDir, Arch: "amd64", Store: store}).Identity()
	if !a.Equal(b) {
		t.Fatalf("identity should be deterministic for unchanged inputs: %s != %s", a, b)
	}
}

// A binary package's identity folds the identities of its locally-built extra
// dependencies, so rebuilding a dependency invalidates the dependent's cache.
func TestBinaryIdentityIncludesDepHashes(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()

	libDir := filepath.Join(root, "pkgs", "libfoo")
	writeSourcePkg(t, libDir, "Source: libfoo\nPackage: libfoo1\nArchitecture: any\n", "")
	appDir := filepath.Join(root, "pkgs", "myapp")
	writeSourcePkg(t, appDir, "Source: myapp\nPackage: myapp-bin\nArchitecture: any\n", "")

	// Build the graph by hand: myapp-bin depends on libfoo1.
	build := func() objstore.Hash {
		lib := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "libfoo", PkgDir: libDir, Arch: "amd64", Store: store})
		libBin := lib.Binary("libfoo1")
		app := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "myapp", PkgDir: appDir, Arch: "amd64", Store: store})
		appBin := app.Binary("myapp-bin")
		appBin.extraDeps = []*debianBinaryPkg{libBin}
		appBin.depsResolved = true
		id, err := appBin.Identity()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	id1 := build()
	// Change libfoo's source.
	writeSourcePkg(t, libDir, "Source: libfoo\nPackage: libfoo1\nArchitecture: any\nBuild-Depends: gcc\n", "")
	id2 := build()

	if id1.Equal(id2) {
		t.Fatalf("binary identity must change when a dependency's source changes: %s == %s", id1, id2)
	}
}

func TestComputeVersionFormat(t *testing.T) {
	store := newStore(t)
	pkgDir := filepath.Join(t.TempDir(), "pkgs", "hello")
	writeSourcePkg(t, pkgDir, "Source: hello\n", "sources: []\n")
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "changelog"),
		[]byte("hello (2.10-3) unstable; urgency=medium\n\n  * Test\n\n -- Dev <d@d>  Mon, 01 Jan 2024 00:00:00 +0000\n"), 0644)

	sb := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "hello", PkgDir: pkgDir, Arch: "amd64", Store: store})
	ver, err := sb.computeVersion()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ver, "2.10-3+gl~") {
		t.Fatalf("version %q does not start with '2.10-3+gl~'", ver)
	}
	suffix := strings.TrimPrefix(ver, "2.10-3+gl~")
	if len(suffix) != 8 {
		t.Fatalf("hash suffix should be 8 chars, got %d: %q", len(suffix), suffix)
	}
	for _, c := range suffix {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("hash suffix should be hex, got %q in %q", string(c), suffix)
		}
	}
}

func TestComputeVersionChangesWithSource(t *testing.T) {
	store := newStore(t)
	pkgDir := filepath.Join(t.TempDir(), "pkgs", "hello")
	writeSourcePkg(t, pkgDir, "Source: hello\n", "sources: []\n")
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "changelog"),
		[]byte("hello (1.0-1) unstable; urgency=medium\n\n  * Init\n\n -- D <d@d>  Mon, 01 Jan 2024 00:00:00 +0000\n"), 0644)

	sb := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "hello", PkgDir: pkgDir, Arch: "amd64", Store: store})
	v1, _ := sb.computeVersion()

	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"), []byte("Source: hello\nBuild-Depends: gcc\n"), 0644)
	sb2 := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "hello", PkgDir: pkgDir, Arch: "amd64", Store: store})
	v2, _ := sb2.computeVersion()

	if v1 == v2 {
		t.Fatalf("version should change when source changes: %s == %s", v1, v2)
	}
	if !strings.HasPrefix(v1, "1.0-1+gl~") || !strings.HasPrefix(v2, "1.0-1+gl~") {
		t.Fatalf("both versions should keep base '1.0-1+gl~': v1=%s v2=%s", v1, v2)
	}
}

func TestParseChangelogTopEntry(t *testing.T) {
	store := newStore(t)
	pkgDir := filepath.Join(t.TempDir(), "pkgs", "openssl")
	writeSourcePkg(t, pkgDir, "Source: openssl\n", "sources: []\n")
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "changelog"),
		[]byte("openssl (3.2.1-1) unstable; urgency=medium\n\n  * New upstream.\n\n -- Foo <f@f>  Mon, 01 Jan 2024 12:34:56 +0000\n"), 0644)

	sb := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "openssl", PkgDir: pkgDir, Arch: "amd64", Store: store})
	name, ver, date, err := sb.parseChangelogTopEntry()
	if err != nil {
		t.Fatal(err)
	}
	if name != "openssl" || ver != "3.2.1-1" || date != "Mon, 01 Jan 2024 12:34:56 +0000" {
		t.Errorf("got (%q,%q,%q)", name, ver, date)
	}
}

func TestParseChangelogTopEntryMalformed(t *testing.T) {
	cases := map[string]string{
		"no parens":              "not a real changelog\n",
		"no trailer":             "openssl (3.2.1-1) unstable; urgency=medium\n\n  * Body but no trailer.\n",
		"no date sep in trailer": "openssl (3.2.1-1) unstable; urgency=medium\n\n  * Body.\n\n -- BadTrailer\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := parseChangelogTopEntryBytes([]byte(body)); err == nil {
				t.Fatalf("expected error for %q, got nil", name)
			}
		})
	}
}

// The synthetic changelog entry has a fixed byte layout, with its trailer date
// mirrored from the top entry so the produced .deb stays reproducible.
func TestFormatLocalChangelogEntry(t *testing.T) {
	got := formatLocalChangelogEntry("openssl", "3.2.1-1+gl~deadbeef", "Mon, 01 Jan 2024 12:34:56 +0000")
	want := "openssl (3.2.1-1+gl~deadbeef) UNRELEASED; urgency=medium\n" +
		"\n" +
		"  * Local build.\n" +
		"\n" +
		" -- nobody <nobody@localhost>  Mon, 01 Jan 2024 12:34:56 +0000\n" +
		"\n"
	if got != want {
		t.Errorf("entry mismatch:\n got: %q\nwant: %q", got, want)
	}
}
