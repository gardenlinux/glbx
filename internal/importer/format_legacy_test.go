package importer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gardenlinux/glbx/internal/objstore"
)

// extractLegacyWithDiff is the only path exercised end-to-end for 1.0+diff
// packages (TestImport10 in import_realnet_test.go). That test depends on
// pcre2 staying 1.0-format upstream and on having network access. These
// fixture-based tests pin the contract so it cannot silently regress.

// stageBlob stores raw bytes in the test store and returns the resulting
// objstore.Hash, mirroring what downloadSourceFiles does for a real
// download.
func stageBlob(t *testing.T, cfg ImportConfig, data []byte) objstore.Hash {
	t.Helper()
	h, err := cfg.Store.Blobs.Store(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("storing blob: %v", err)
	}
	return h
}

// legacyFixture is a fully-staged 1.0+diff fixture: orig tarball + .diff.gz
// already in the store, plus the `pkg` and `files` map values that
// extractLegacyWithDiff expects.
type legacyFixture struct {
	pkg      *sourcePackage
	files    map[string]objstore.Hash
	diffName string
	origName string
}

// buildLegacyFixture builds an in-memory Format: 1.0 + diff package.
//
//	a/Makefile                 ← from the orig tarball
//	b/Makefile                 ← Makefile after the diff (when withNonDebian)
//	b/debian/{control,changelog}  ← created by the diff
//
// includeNonDebianChange controls whether the diff touches Makefile too —
// the toggle exercises both branches of the post-extraction "are there
// non-debian changes?" check inside extractLegacyWithDiff.
func buildLegacyFixture(t *testing.T, cfg ImportConfig, includeNonDebianChange bool) legacyFixture {
	t.Helper()

	const origName = "testpkg_1.0.orig.tar.gz"
	const diffName = "testpkg_1.0-1.diff.gz"

	origData := createTarGz(t, map[string]string{
		"testpkg-1.0/":         "",
		"testpkg-1.0/Makefile": "all:\n\techo hello\n",
	})
	origHash := stageBlob(t, cfg, origData)

	// Hand-craft a unified diff. patch is invoked with -p1 from dirB, so
	// the leading "a/" / "b/" prefix is stripped. dirB starts as a clone
	// of dirA; "/dev/null"-style chunks would also work but the
	// "@@ -0,0 +N,M @@" form is what `dpkg-source -b` actually emits.
	var diff bytes.Buffer
	if includeNonDebianChange {
		diff.WriteString("--- a/Makefile\t1970-01-01 00:00:00.000000000 +0000\n")
		diff.WriteString("+++ b/Makefile\t1970-01-01 00:00:00.000000000 +0000\n")
		diff.WriteString("@@ -1,2 +1,2 @@\n")
		diff.WriteString(" all:\n")
		diff.WriteString("-\techo hello\n")
		diff.WriteString("+\techo patched\n")
	}
	diff.WriteString("--- a/debian/control\t1970-01-01 00:00:00.000000000 +0000\n")
	diff.WriteString("+++ b/debian/control\t1970-01-01 00:00:00.000000000 +0000\n")
	diff.WriteString("@@ -0,0 +1,2 @@\n")
	diff.WriteString("+Source: testpkg\n")
	diff.WriteString("+Maintainer: nobody <nobody@localhost>\n")
	diff.WriteString("--- a/debian/changelog\t1970-01-01 00:00:00.000000000 +0000\n")
	diff.WriteString("+++ b/debian/changelog\t1970-01-01 00:00:00.000000000 +0000\n")
	diff.WriteString("@@ -0,0 +1,5 @@\n")
	diff.WriteString("+testpkg (1.0-1) unstable; urgency=low\n")
	diff.WriteString("+\n")
	diff.WriteString("+  * Initial.\n")
	diff.WriteString("+\n")
	diff.WriteString("+ -- nobody <nobody@localhost>  Thu, 01 Jan 1970 00:00:00 +0000\n")

	diffHash := stageBlob(t, cfg, createGzipData(t, diff.Bytes()))

	pkg := &sourcePackage{
		Name:    "testpkg",
		Version: "1.0-1",
		Format:  "1.0",
		Files: []sourceFile{
			{Hash: origHash.String(), Size: "0", Name: origName},
			{Hash: diffHash.String(), Size: "0", Name: diffName},
		},
	}
	files := map[string]objstore.Hash{
		origName: origHash,
		diffName: diffHash,
	}
	return legacyFixture{pkg: pkg, files: files, diffName: diffName, origName: origName}
}

func newLegacyTestConfig(t *testing.T) (ImportConfig, string) {
	t.Helper()
	store := setupTestStore(t)
	outDir := t.TempDir()
	cfg := ImportConfig{
		Ctx:       context.Background(),
		Store:     store,
		OutputDir: outDir,
		NoVerify:  true,
	}
	return cfg, outDir
}

// TestExtractLegacyWithDiffSplitsDebianFromNonDebian is the central pin.
// extractLegacyWithDiff must:
//
//  1. Lay debian/ down at pkgDir/src/debian/ from what the diff created.
//  2. Synthesize debian/patches/series + debian.patch from the NON-debian/
//     parts of the diff, so the rest of the pipeline (which expects a
//     quilt-shaped layout) can use it uniformly.
//  3. Leave the patch out when the diff has no non-debian changes
//     (covered by TestExtractLegacyWithDiffNoNonDebianChanges).
func TestExtractLegacyWithDiffSplitsDebianFromNonDebian(t *testing.T) {
	cfg, outDir := newLegacyTestConfig(t)

	fx := buildLegacyFixture(t, cfg, true)

	pkgDir := filepath.Join(outDir, "pkgs", fx.pkg.Name)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir pkgDir: %v", err)
	}

	if err := extractLegacyWithDiff(cfg, fx.pkg, fx.files, pkgDir, fx.diffName); err != nil {
		t.Fatalf("extractLegacyWithDiff: %v", err)
	}

	// debian/control + debian/changelog must be present from the diff.
	for _, rel := range []string{"src/debian/control", "src/debian/changelog"} {
		full := filepath.Join(pkgDir, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("missing %s: %v", rel, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", rel)
		}
	}

	// patches/series must list debian.patch, and debian.patch must
	// contain the Makefile change but NOT the debian/ creation lines.
	seriesPath := filepath.Join(pkgDir, "src", "debian", "patches", "series")
	seriesData, err := os.ReadFile(seriesPath)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if !strings.Contains(string(seriesData), "debian.patch") {
		t.Errorf("series missing debian.patch: %q", string(seriesData))
	}

	patchPath := filepath.Join(pkgDir, "src", "debian", "patches", "debian.patch")
	patchData, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("debian.patch: %v", err)
	}
	if !strings.Contains(string(patchData), "Makefile") {
		t.Errorf("debian.patch should contain Makefile change, got:\n%s", string(patchData))
	}
	// extractLegacyWithDiff removes a/debian and b/debian BEFORE diffing
	// to generate the non-debian patch — so the synthesized patch must
	// not mention debian/ paths.
	if strings.Contains(string(patchData), "debian/control") || strings.Contains(string(patchData), "debian/changelog") {
		t.Errorf("debian.patch should NOT include debian/ paths, got:\n%s", string(patchData))
	}
}

// TestExtractLegacyWithDiffNoNonDebianChanges confirms that when a 1.0+diff
// package's diff touches only debian/, NO debian/patches/series is written.
// The downstream build phase distinguishes "missing series" from "empty
// series", so this branch matters.
func TestExtractLegacyWithDiffNoNonDebianChanges(t *testing.T) {
	cfg, outDir := newLegacyTestConfig(t)

	fx := buildLegacyFixture(t, cfg, false)

	pkgDir := filepath.Join(outDir, "pkgs", fx.pkg.Name)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir pkgDir: %v", err)
	}

	if err := extractLegacyWithDiff(cfg, fx.pkg, fx.files, pkgDir, fx.diffName); err != nil {
		t.Fatalf("extractLegacyWithDiff: %v", err)
	}

	// debian/control must still land — the diff's debian/ portion was
	// applied — but no patches/ should exist because the non-debian diff
	// is empty.
	if _, err := os.Stat(filepath.Join(pkgDir, "src", "debian", "control")); err != nil {
		t.Errorf("debian/control missing: %v", err)
	}
	patchesDir := filepath.Join(pkgDir, "src", "debian", "patches")
	if _, err := os.Stat(patchesDir); !os.IsNotExist(err) {
		t.Errorf("debian/patches should not exist when diff has no non-debian changes (stat err = %v)", err)
	}
}

// TestExtractLegacyWithDiffMissingOrig confirms a clear error when the
// orig tarball file isn't in the downloaded set. Important because
// downloadSourceFiles populates this map at runtime and a mistake there
// would surface here.
func TestExtractLegacyWithDiffMissingOrig(t *testing.T) {
	cfg, outDir := newLegacyTestConfig(t)

	fx := buildLegacyFixture(t, cfg, true)
	delete(fx.files, fx.origName)

	pkgDir := filepath.Join(outDir, "pkgs", fx.pkg.Name)
	_ = os.MkdirAll(pkgDir, 0o755)

	err := extractLegacyWithDiff(cfg, fx.pkg, fx.files, pkgDir, fx.diffName)
	if err == nil {
		t.Fatal("expected error when orig tarball missing from files map, got nil")
	}
	if !strings.Contains(err.Error(), "orig tarball") {
		t.Errorf("error should name orig tarball, got: %v", err)
	}
}

// TestExtractLegacyWithDiffMissingDiff confirms the symmetric case for the
// .diff.gz file.
func TestExtractLegacyWithDiffMissingDiff(t *testing.T) {
	cfg, outDir := newLegacyTestConfig(t)

	fx := buildLegacyFixture(t, cfg, true)
	delete(fx.files, fx.diffName)

	pkgDir := filepath.Join(outDir, "pkgs", fx.pkg.Name)
	_ = os.MkdirAll(pkgDir, 0o755)

	err := extractLegacyWithDiff(cfg, fx.pkg, fx.files, pkgDir, fx.diffName)
	if err == nil {
		t.Fatal("expected error when diff file missing from files map, got nil")
	}
	if !strings.Contains(err.Error(), "diff file") {
		t.Errorf("error should name diff file, got: %v", err)
	}
}

// TestExtractLegacyWithDiffDeterministic pins that two back-to-back imports
// of the same 1.0+diff package produce a byte-identical debian.patch.
// Regression: diff(1) emits wall-clock mtimes in the unified-header
// "--- path\tTIMESTAMP" / "+++ path\tTIMESTAMP" lines. Those mtimes come
// from copyDir (which doesn't preserve them) and patch(1) (which sets them
// to now), so the patch text used to drift between runs even when input
// was identical. extractLegacyWithDiff must strip the timestamps.
func TestExtractLegacyWithDiffDeterministic(t *testing.T) {
	run := func() []byte {
		cfg, outDir := newLegacyTestConfig(t)
		fx := buildLegacyFixture(t, cfg, true)

		pkgDir := filepath.Join(outDir, "pkgs", fx.pkg.Name)
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatalf("mkdir pkgDir: %v", err)
		}
		if err := extractLegacyWithDiff(cfg, fx.pkg, fx.files, pkgDir, fx.diffName); err != nil {
			t.Fatalf("extractLegacyWithDiff: %v", err)
		}
		patchPath := filepath.Join(pkgDir, "src", "debian", "patches", "debian.patch")
		data, err := os.ReadFile(patchPath)
		if err != nil {
			t.Fatalf("read debian.patch: %v", err)
		}
		return data
	}

	first := run()
	// Sleep 1.1s between runs so that any wall-clock-second-resolution
	// timestamps that slipped through would differ between runs.
	time.Sleep(1100 * time.Millisecond)
	second := run()

	if !bytes.Equal(first, second) {
		t.Errorf("debian.patch differed between runs\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	// Belt-and-braces: no `---`/`+++` line should carry a tab — that's
	// where diff(1) tucks the timestamp.
	for _, line := range bytes.Split(first, []byte("\n")) {
		if (bytes.HasPrefix(line, []byte("--- ")) || bytes.HasPrefix(line, []byte("+++ "))) && bytes.IndexByte(line, '\t') >= 0 {
			t.Errorf("header line still has tab/timestamp: %q", string(line))
		}
	}
}

// TestStripDiffTimestamps unit-tests the header-rewrite helper directly so
// regressions don't need a full extractLegacyWithDiff round-trip to surface.
func TestStripDiffTimestamps(t *testing.T) {
	in := []byte("--- a/foo\t2026-05-27 23:02:52.948884638 +0000\n" +
		"+++ b/foo\t2026-05-27 23:02:52.948884638 +0000\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n" +
		"--- a/bar\t1970-01-01 00:00:00 +0000\n" +
		"+++ b/bar\n")
	want := []byte("--- a/foo\n" +
		"+++ b/foo\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n" +
		"--- a/bar\n" +
		"+++ b/bar\n")
	got := stripDiffTimestamps(in)
	if !bytes.Equal(got, want) {
		t.Errorf("stripDiffTimestamps mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// branch where a 1.0 package has no .diff.gz at all (1.0 native). The
// existing 3.0-native real-network test (TestImportNative3) covers the
// extractor itself; this test pins the dispatch decision so a future
// refactor that breaks the "if no diff, fall through to extractNative"
// shortcut would be caught here and not in slow real-network tests.
func TestExtractLegacyDispatchesToNativeWhenNoDiff(t *testing.T) {
	cfg, outDir := newLegacyTestConfig(t)

	const nativeName = "testpkg_1.0.tar.gz"
	nativeData := createTarGz(t, map[string]string{
		"testpkg-1.0/":               "",
		"testpkg-1.0/Makefile":       "all:\n\techo hello\n",
		"testpkg-1.0/debian/":        "",
		"testpkg-1.0/debian/control": "Source: testpkg\nMaintainer: nobody <nobody@localhost>\n",
	})
	nativeHash := stageBlob(t, cfg, nativeData)

	pkg := &sourcePackage{
		Name:    "testpkg",
		Version: "1.0",
		Format:  "1.0",
		Files: []sourceFile{
			{Hash: nativeHash.String(), Size: "0", Name: nativeName},
		},
	}
	files := map[string]objstore.Hash{nativeName: nativeHash}

	pkgDir := filepath.Join(outDir, "pkgs", pkg.Name)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir pkgDir: %v", err)
	}

	if err := extractLegacy(cfg, pkg, files, pkgDir); err != nil {
		t.Fatalf("extractLegacy on 1.0-native: %v", err)
	}

	// debian/control must come from the single tarball — confirms the
	// extractor delegated to extractNative, not extractLegacyWithDiff.
	if _, err := os.Stat(filepath.Join(pkgDir, "src", "debian", "control")); err != nil {
		t.Errorf("expected debian/control from native tarball, got: %v", err)
	}
}
