package importer

// import_realnet_test.go covers source-only imports (no lockfile, no build)
// against the real Debian testing repository. The four format paths through
// the importer are otherwise only exercised by hand-crafted httptest fixtures
// (see import_test.go) — those catch logic bugs but not "the dsc parser
// breaks on a real-world quirk we didn't anticipate" bugs.
//
// One real package per format:
//
//   - 3.0 (native)        → base-files
//   - 3.0 (quilt)         → coreutils
//   - 1.0 (with diff.gz)  → authbind
//   - 3.0 (quilt) + addon orig (multi-orig) → perl
//
// The multi-orig case is the only one that asserts more than basic happy-path
// extraction: it pins the contract that addon origs DO appear in sources.yml
// (so the build phase can extract them) and that their blobs land in the
// store. Without that, perl and other source packages with split origs would
// silently drop their addon orig at import time and fail much later.
//
// All tests share one objstore so the InRelease + Sources index download is
// paid once per `go test` invocation, not once per format.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gardenlinux/glbx/internal/objstore"
)

type realnetImportState struct {
	once     sync.Once
	store    *objstore.Store
	storeDir string
	setupErr error
}

var sharedRealnet realnetImportState

// ensureRealnetState opens the shared store. It does NOT prefetch any index —
// the importer will populate the store on the first test that calls Import,
// and subsequent tests reuse those blobs (Release, Sources.xz). A persistent
// cache directory may be provided via GL_IMPORT_CACHE_DIR.
func ensureRealnetState() {
	sharedRealnet.once.Do(func() {
		dir := os.Getenv("GL_IMPORT_CACHE_DIR")
		if dir == "" {
			d, err := os.MkdirTemp("", "glbx-import-e2e-")
			if err != nil {
				sharedRealnet.setupErr = err
				return
			}
			dir = d
		} else {
			if err := os.MkdirAll(dir, 0755); err != nil {
				sharedRealnet.setupErr = err
				return
			}
		}
		sharedRealnet.storeDir = dir

		store, err := objstore.NewLocal(dir)
		if err != nil {
			sharedRealnet.setupErr = err
			return
		}
		sharedRealnet.store = store
	})
}

// requireRealnet skips the test under -short, when no network is available,
// or when the shared store cannot be opened.
func requireRealnet(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping real-network import test in -short mode")
	}
	ensureRealnetState()
	if sharedRealnet.setupErr != nil {
		t.Skipf("realnet setup failed: %v", sharedRealnet.setupErr)
	}
	// Best-effort connectivity probe so a no-network environment skips
	// rather than fails. The first import would fail anyway, but a probe
	// gives a clearer skip reason.
	resp, err := http.Head("https://deb.debian.org/debian/dists/testing/InRelease")
	if err != nil {
		t.Skipf("no network to deb.debian.org: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Skipf("deb.debian.org returned %d", resp.StatusCode)
	}
}

// runImport is a thin wrapper that runs the importer against the real Debian
// testing archive with NoVerify=true (signature verification is exercised by
// the keyring-based unit tests; here we focus on the format-extraction code
// paths). Each test gets its own OutputDir so the on-disk pkgs/ layouts don't
// collide.
func runImport(t *testing.T, pkgName string) (*ImportResult, string) {
	t.Helper()
	outputDir := t.TempDir()
	cfg := ImportConfig{
		Ctx:       context.Background(),
		Store:     sharedRealnet.store,
		OutputDir: outputDir,
		NoVerify:  true,
	}
	res, err := Import(cfg, pkgName)
	if err != nil {
		t.Fatalf("Import(%q) failed: %v", pkgName, err)
	}
	return res, outputDir
}

// assertSourcesYMLContains verifies sources.yml on disk lists every expected
// entry by name AND by hash. The result struct is the importer's in-memory
// view; sources.yml is what every downstream tool (build phase, hash audits)
// reads. They MUST agree, so we check both.
func assertSourcesYMLContains(t *testing.T, outputDir, pkgName string, want []SourceEntry) {
	t.Helper()
	path := filepath.Join(outputDir, "pkgs", pkgName, "sources.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	parsed, err := ParseSourcesYML(data)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	if len(parsed) != len(want) {
		t.Fatalf("sources.yml has %d entries, want %d: parsed=%v want=%v", len(parsed), len(want), parsed, want)
	}
	wantByName := make(map[string]objstore.Hash, len(want))
	for _, e := range want {
		wantByName[e.Name] = e.Hash
	}
	for _, got := range parsed {
		wantHash, ok := wantByName[got.Name]
		if !ok {
			t.Errorf("sources.yml has unexpected entry %q", got.Name)
			continue
		}
		if !got.Hash.Equal(wantHash) {
			t.Errorf("sources.yml entry %q: hash %s, want %s", got.Name, got.Hash, wantHash)
		}
	}
}

// assertControlExists checks that pkgs/<name>/src/debian/control was
// extracted and contains a Source: stanza for the package — a sanity check
// that the format-specific extractor actually wrote the debian/ directory.
func assertControlExists(t *testing.T, outputDir, pkgName string) {
	t.Helper()
	controlPath := filepath.Join(outputDir, "pkgs", pkgName, "src", "debian", "control")
	data, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatalf("reading debian/control for %s: %v", pkgName, err)
	}
	if !strings.Contains(string(data), "Source: "+pkgName) {
		t.Errorf("debian/control for %s missing Source: stanza", pkgName)
	}
}

// TestImportNative3 covers Format: 3.0 (native). base-files is the canonical
// native package: a single tarball that contains the whole source tree
// including debian/. There is no orig tarball, so sources.yml must be absent.
func TestImportNative3(t *testing.T) {
	requireRealnet(t)

	res, outputDir := runImport(t, "base-files")

	if res.Format != "3.0 (native)" {
		t.Errorf("expected Format 3.0 (native), got %q", res.Format)
	}
	if len(res.Sources) != 0 {
		t.Errorf("native package should have no orig tarballs in result, got %d", len(res.Sources))
	}

	assertControlExists(t, outputDir, "base-files")

	// Native packages must NOT produce a sources.yml — the build phase
	// distinguishes "missing sources.yml" from "empty sources.yml" and uses
	// it to decide whether to expect orig tarballs at build time.
	sourcesYML := filepath.Join(outputDir, "pkgs", "base-files", "sources.yml")
	if _, err := os.Stat(sourcesYML); !os.IsNotExist(err) {
		t.Errorf("native package should not have sources.yml: stat err = %v", err)
	}
}

// TestImportQuilt3 covers Format: 3.0 (quilt) — the dominant Debian source
// format. coreutils is large enough to be representative but not huge.
// extractQuilt extracts only debian.tar.* at import time; the orig tarball
// is stored as a blob and listed in sources.yml for the build phase to use.
func TestImportQuilt3(t *testing.T) {
	requireRealnet(t)

	res, outputDir := runImport(t, "coreutils")

	if res.Format != "3.0 (quilt)" {
		t.Errorf("expected Format 3.0 (quilt), got %q", res.Format)
	}
	if len(res.Sources) != 1 {
		t.Fatalf("quilt single-orig package should have 1 source entry, got %d", len(res.Sources))
	}
	if !strings.Contains(res.Sources[0].Name, ".orig.tar.") {
		t.Errorf("source entry name should be the orig tarball, got %q", res.Sources[0].Name)
	}
	if !sharedRealnet.store.Blobs.Has(res.Sources[0].Hash) {
		t.Errorf("orig tarball blob %s missing from store", res.Sources[0].Hash)
	}

	assertControlExists(t, outputDir, "coreutils")
	assertSourcesYMLContains(t, outputDir, "coreutils", res.Sources)

	// Quilt packages also produce debian/rules and debian/changelog from the
	// .debian.tar.* — check at least one to confirm the tarball was actually
	// extracted, not just touched.
	rulesPath := filepath.Join(outputDir, "pkgs", "coreutils", "src", "debian", "rules")
	if _, err := os.Stat(rulesPath); err != nil {
		t.Errorf("debian/rules missing for coreutils: %v", err)
	}
}

// TestImport10 covers Format: 1.0 with a separate .diff.gz of Debian-specific
// changes against the upstream tarball. pcre2 is the canonical 1.0+diff
// package in current Debian testing; if it ever migrates to 3.0 (quilt) this
// test will need to pick another (`Sources.xz | grep -B1 'Format: 1.0'` and
// confirm the .dsc Files: section lists a .diff.gz).
//
// Note: not every Format: 1.0 package has a diff — some are "1.0 native"
// (single tarball, no orig/diff split, e.g. authbind). That code path is
// already covered by the 3.0 (native) test since extractLegacy delegates
// to extractNative when no diff is present.
//
// extractLegacy applies the diff via patch -p1 and synthesizes a quilt
// patch series so the rest of the pipeline (which expects quilt) still
// works. The check here is that debian/control landed and the orig is in
// sources.yml so the build phase can extract it.
func TestImport10(t *testing.T) {
	requireRealnet(t)

	res, outputDir := runImport(t, "pcre2")

	if res.Format != "1.0" {
		t.Errorf("expected Format 1.0, got %q", res.Format)
	}
	if len(res.Sources) != 1 {
		t.Fatalf("1.0+diff package should have 1 source entry (the orig), got %d", len(res.Sources))
	}
	if !strings.Contains(res.Sources[0].Name, ".orig.tar.") {
		t.Errorf("source entry name should be the orig tarball, got %q", res.Sources[0].Name)
	}
	if !sharedRealnet.store.Blobs.Has(res.Sources[0].Hash) {
		t.Errorf("orig tarball blob %s missing from store", res.Sources[0].Hash)
	}

	assertControlExists(t, outputDir, "pcre2")
	assertSourcesYMLContains(t, outputDir, "pcre2", res.Sources)

	// The whole point of extractLegacyWithDiff is that 1.0+diff packages are
	// converted into a quilt-shaped layout downstream tools can treat
	// uniformly: a debian/ tree plus debian/patches/{series, debian.patch}
	// containing all non-debian/ changes from the .diff.gz. pcre2's diff
	// definitely touches non-debian/ files (it patches build system &
	// configure), so series + debian.patch MUST exist. If a future pcre2
	// release moves all its changes inside debian/ this assertion will need
	// to soften — but treat that as a signal to confirm a 1.0+diff with
	// non-debian changes is still being tested.
	patchesDir := filepath.Join(outputDir, "pkgs", "pcre2", "src", "debian", "patches")
	seriesPath := filepath.Join(patchesDir, "series")
	patchPath := filepath.Join(patchesDir, "debian.patch")
	if _, err := os.Stat(seriesPath); err != nil {
		t.Errorf("debian/patches/series missing — 1.0→quilt conversion did not synthesize patch series: %v", err)
	} else {
		seriesData, _ := os.ReadFile(seriesPath)
		if !strings.Contains(string(seriesData), "debian.patch") {
			t.Errorf("series file should reference debian.patch, got: %q", string(seriesData))
		}
	}
	if info, err := os.Stat(patchPath); err != nil {
		t.Errorf("debian/patches/debian.patch missing: %v", err)
	} else if info.Size() == 0 {
		t.Errorf("debian/patches/debian.patch is empty — diff was not captured")
	}
}

// TestImportMultiOrig pins the multi-orig contract end-to-end at import time.
// perl 5.40.1-7 ships TWO orig tarballs:
//
//   - perl_5.40.1.orig.tar.xz
//   - perl_5.40.1.orig-regen-configure.tar.xz   (an addon component)
//
// Both must show up in sources.yml so the build phase (build_phases.go:144)
// can mount and extract each one into <srcDst>/<component>/. If either drops
// out at import time, perl will silently fail to build with a "regen-configure
// missing" error far from the cause.
//
// This is the only realnet test that actually asserts a non-trivial property
// of the importer — the others just check happy-path extraction.
func TestImportMultiOrig(t *testing.T) {
	requireRealnet(t)

	res, outputDir := runImport(t, "perl")

	if res.Format != "3.0 (quilt)" {
		t.Errorf("expected Format 3.0 (quilt) for perl, got %q", res.Format)
	}
	if len(res.Sources) < 2 {
		t.Fatalf("perl is multi-orig — expected ≥2 source entries, got %d (%v)", len(res.Sources), res.Sources)
	}

	// Find both the main orig and at least one .orig-COMPONENT entry.
	var hasMain, hasAddon bool
	for _, s := range res.Sources {
		switch {
		case strings.Contains(s.Name, ".orig-"):
			hasAddon = true
		case strings.Contains(s.Name, ".orig.tar."):
			hasMain = true
		}
		if !sharedRealnet.store.Blobs.Has(s.Hash) {
			t.Errorf("source %s blob %s missing from store", s.Name, s.Hash)
		}
	}
	if !hasMain {
		t.Errorf("perl sources missing main .orig.tar.*: %v", res.Sources)
	}
	if !hasAddon {
		t.Errorf("perl sources missing addon .orig-*.tar.*: %v", res.Sources)
	}

	// And the same set must round-trip through sources.yml on disk so
	// downstream tools (and the build phase) see what the importer
	// returned in-memory.
	assertSourcesYMLContains(t, outputDir, "perl", res.Sources)

	assertControlExists(t, outputDir, "perl")
}
