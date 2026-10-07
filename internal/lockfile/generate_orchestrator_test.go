package lockfile

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// generate_orchestrator_test.go drives the three top-level orchestrators —
// FetchBinaryIndex, Generate, and GenerateRootfs — against a synthetic
// httptest server, exercising the orchestration glue (URL composition, hash
// caching, output-file plumbing) without the network.

// fakeRepo is a minimal Debian-style apt repo backed by httptest. It serves
// /dists/<dist>/InRelease, /dists/<dist>/main/binary-<arch>/Packages.gz,
// and arbitrary .deb paths registered via addDeb().
type fakeRepo struct {
	t       *testing.T
	srv     *httptest.Server
	dist    string
	arch    string
	debs    map[string][]byte // pool path → bytes
	debHits atomic.Int32      // .deb fetches
	idxHits atomic.Int32      // Packages.gz fetches
	relHits atomic.Int32      // InRelease fetches

	// Built lazily on first request so callers can register packages
	// after newFakeRepo() returns.
	packagesGz []byte
	inRelease  []byte
}

// fakePkg is a single binary package we want available in the fake index.
type fakePkg struct {
	name      string
	version   string
	depends   string // optional, e.g. "libfoo (>= 1.0)"
	essential bool
	debBody   []byte // .deb file content (synthetic — never extracted)
}

func newFakeRepo(t *testing.T, dist, arch string, pkgs []fakePkg) *fakeRepo {
	t.Helper()
	fr := &fakeRepo{
		t:    t,
		dist: dist,
		arch: arch,
		debs: make(map[string][]byte),
	}

	// Build .deb files and the Packages stanza set first.
	var packages bytes.Buffer
	for i, p := range pkgs {
		body := p.debBody
		if body == nil {
			body = []byte(fmt.Sprintf("synthetic-deb-%s-%d", p.name, i))
		}
		poolPath := fmt.Sprintf("pool/main/%c/%s/%s_%s_%s.deb",
			p.name[0], p.name, p.name, p.version, arch)
		fr.debs[poolPath] = body

		fmt.Fprintf(&packages, "Package: %s\n", p.name)
		fmt.Fprintf(&packages, "Version: %s\n", p.version)
		fmt.Fprintf(&packages, "Architecture: %s\n", arch)
		if p.depends != "" {
			fmt.Fprintf(&packages, "Depends: %s\n", p.depends)
		}
		if p.essential {
			fmt.Fprintln(&packages, "Essential: yes")
		}
		fmt.Fprintf(&packages, "Filename: %s\n", poolPath)
		fmt.Fprintf(&packages, "Size: %d\n", len(body))
		fmt.Fprintf(&packages, "SHA256: %s\n", sha256Hex(body))
		fmt.Fprintf(&packages, "Description: synthetic %s\n\n", p.name)
	}

	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	if _, err := gw.Write(packages.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	fr.packagesGz = gz.Bytes()
	packagesGzHash := sha256Hex(fr.packagesGz)

	// Build the (forged-signed) InRelease referencing Packages.gz by hash.
	releaseStanza := fmt.Sprintf("Origin: Test\nSuite: %s\nSHA256:\n %s %d main/binary-%s/Packages.gz\n",
		dist, packagesGzHash, len(fr.packagesGz), arch)
	fr.inRelease = []byte("-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\n" +
		releaseStanza +
		"-----BEGIN PGP SIGNATURE-----\nfakesig\n-----END PGP SIGNATURE-----\n")

	fr.srv = httptest.NewServer(http.HandlerFunc(fr.serve))
	t.Cleanup(fr.srv.Close)
	return fr
}

func (fr *fakeRepo) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case path == fmt.Sprintf("dists/%s/InRelease", fr.dist):
		fr.relHits.Add(1)
		w.Write(fr.inRelease)
	case path == fmt.Sprintf("dists/%s/main/binary-%s/Packages.gz", fr.dist, fr.arch):
		fr.idxHits.Add(1)
		w.Write(fr.packagesGz)
	default:
		// .deb fetch?
		if data, ok := fr.debs[path]; ok {
			fr.debHits.Add(1)
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}
}

// =============================================================================
// FetchBinaryIndex
// =============================================================================

func TestFetchBinaryIndexHappyPath(t *testing.T) {
	fr := newFakeRepo(t, "testing", "amd64", []fakePkg{
		{name: "alpha", version: "1.0"},
		{name: "beta", version: "2.0", essential: true},
	})

	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	idx, err := FetchBinaryIndex(context.Background(), store, fr.srv.URL, "testing", "amd64", "")
	if err != nil {
		t.Fatalf("FetchBinaryIndex: %v", err)
	}

	// Sanity-check the parsed index. The Find/EssentialPackages surface
	// is enough — we don't need to pin internal stanza shape.
	if alpha := idx.Get("alpha"); alpha == nil || alpha.Version != "1.0" {
		t.Errorf("Get(alpha) = %+v, want version 1.0", alpha)
	}
	ess := idx.EssentialPackages()
	if len(ess) != 1 || ess[0].Name != "beta" {
		t.Errorf("EssentialPackages = %v, want [beta]", ess)
	}
}

func TestFetchBinaryIndexCachesPackagesGz(t *testing.T) {
	fr := newFakeRepo(t, "testing", "amd64", []fakePkg{
		{name: "alpha", version: "1.0"},
	})

	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := FetchBinaryIndex(context.Background(), store, fr.srv.URL, "testing", "amd64", ""); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	firstIdxHits := fr.idxHits.Load()
	if firstIdxHits != 1 {
		t.Fatalf("expected 1 Packages.gz hit after first call, got %d", firstIdxHits)
	}

	// Second call must read Packages.gz from the blob cache (the InRelease
	// path doesn't have a cookie set, so it WILL be re-fetched — that's
	// expected and not what we're testing here).
	if _, err := FetchBinaryIndex(context.Background(), store, fr.srv.URL, "testing", "amd64", ""); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := fr.idxHits.Load(); got != firstIdxHits {
		t.Errorf("Packages.gz fetched again on second call: hits %d → %d", firstIdxHits, got)
	}
}

func TestFetchBinaryIndexHashMismatch(t *testing.T) {
	fr := newFakeRepo(t, "testing", "amd64", []fakePkg{
		{name: "alpha", version: "1.0"},
	})
	// Tamper with the served Packages.gz so it no longer matches the
	// hash advertised in InRelease.
	fr.packagesGz = append(fr.packagesGz, 0x00)

	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	_, err = FetchBinaryIndex(context.Background(), store, fr.srv.URL, "testing", "amd64", "")
	if err == nil {
		t.Fatal("expected hash-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("error should mention hash mismatch, got: %v", err)
	}
}

func TestFetchBinaryIndexMissingFromRelease(t *testing.T) {
	// Ask for arch=arm64 but the InRelease only advertises amd64 →
	// FetchBinaryIndex must surface a clear error rather than e.g.
	// silently fetching a 404.
	fr := newFakeRepo(t, "testing", "amd64", []fakePkg{
		{name: "alpha", version: "1.0"},
	})

	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	_, err = FetchBinaryIndex(context.Background(), store, fr.srv.URL, "testing", "arm64", "")
	if err == nil {
		t.Fatal("expected error when arch's Packages.gz is not in Release, got nil")
	}
	if !strings.Contains(err.Error(), "Packages.gz") && !strings.Contains(err.Error(), "binary-arm64") {
		t.Errorf("error should mention the missing path, got: %v", err)
	}
}

// =============================================================================
// Generate (full orchestration: fetch index → resolve → fetch debs → write yml)
// =============================================================================

// genFixture lists the packages the synthetic Generate test relies on. The
// build-deps closure is intentionally tiny so the resolver finishes fast and
// the test stays readable. buildResolverRoots adds build-essential, fakeroot,
// and debconf as implicit roots, so those MUST exist in the fake index.
func genFixturePkgs() []fakePkg {
	return []fakePkg{
		// implicit roots from buildResolverRoots
		{name: "build-essential", version: "12.10"},
		{name: "fakeroot", version: "1.31"},
		{name: "debconf", version: "1.5"},
		// declared Build-Depends in the synthetic control file
		{name: "libfoo-dev", version: "1.0"},
		// one Essential pkg for variety
		{name: "core-libs", version: "1.0", essential: true},
	}
}

// stageControlFile writes a minimal debian/control under
// outputDir/pkgs/<name>/src/debian/control. extractBuildDeps reads from this
// path during Generate.
func stageControlFile(t *testing.T, outputDir, pkgName, buildDeps string) {
	t.Helper()
	dir := filepath.Join(outputDir, "pkgs", pkgName, "src", "debian")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	control := fmt.Sprintf("Source: %s\nBuild-Depends: %s\n\nPackage: %s\nArchitecture: any\n", pkgName, buildDeps, pkgName)
	if err := os.WriteFile(filepath.Join(dir, "control"), []byte(control), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateEndToEnd(t *testing.T) {
	fr := newFakeRepo(t, "testing", "amd64", genFixturePkgs())

	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	outputDir := t.TempDir()
	stageControlFile(t, outputDir, "mypkg", "libfoo-dev")

	res, err := Generate(Config{
		Ctx:       context.Background(),
		Store:     store,
		RepoURL:   fr.srv.URL,
		Dist:      "testing",
		Arch:      "amd64",
		OutputDir: outputDir,
		PkgName:   "mypkg",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Result invariants.
	if res.Arch != "amd64" {
		t.Errorf("res.Arch = %q, want amd64", res.Arch)
	}
	if res.Packages == 0 {
		t.Errorf("res.Packages = 0; expected at least the implicit roots")
	}

	// build-deps.yml is the explicit tooling list, readable by buildcfg.
	depsYML := filepath.Join(outputDir, "pkgs", "mypkg", "build-deps.yml")
	tools, err := buildcfg.LoadBuildDeps(depsYML)
	if err != nil {
		t.Fatalf("load build-deps.yml: %v", err)
	}
	if len(tools) != res.Packages {
		t.Errorf("build-deps.yml has %d tools, want %d", len(tools), res.Packages)
	}
	byName := map[string]buildcfg.PinnedTool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	for _, want := range []string{"build-essential", "libfoo-dev"} {
		tool, ok := byName[want]
		if !ok {
			t.Errorf("build-deps.yml missing %q; tools: %v", want, byName)
			continue
		}
		if len(tool.Files) != 1 || tool.Files[0].Arch != "amd64" || len(tool.Files[0].URLs) == 0 {
			t.Errorf("tool %q has malformed file entry: %+v", want, tool.Files)
		}
		if !store.Blobs.Has(tool.Files[0].Hash) {
			t.Errorf("tool %q .deb blob not in store", want)
		}
	}

	// The .deb files must have been fetched and stored.
	if got := fr.debHits.Load(); got == 0 {
		t.Errorf(".deb fetch never happened — FetchDebs not wired through")
	}
	for poolPath, body := range fr.debs {
		h, _ := objstore.NewHash(sha256Hex(body))
		if !store.Blobs.Has(h) {
			t.Errorf(".deb blob missing for pool path %s", poolPath)
		}
	}
}

func TestGenerateRejectsMissingControl(t *testing.T) {
	fr := newFakeRepo(t, "testing", "amd64", genFixturePkgs())
	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outputDir := t.TempDir()
	// No control file staged — Generate must error before any HTTP
	// activity for the binary index.
	_, err = Generate(Config{
		Ctx:       context.Background(),
		Store:     store,
		RepoURL:   fr.srv.URL,
		Dist:      "testing",
		Arch:      "amd64",
		OutputDir: outputDir,
		PkgName:   "ghost",
	})
	if err == nil {
		t.Fatal("expected error when control file is missing")
	}
	if !strings.Contains(err.Error(), "extract build-deps") {
		t.Errorf("error should mention extract build-deps stage, got: %v", err)
	}
}

// =============================================================================
// GenerateRootfs (uses the same fetch+resolve+store pipeline but with a
// hard-coded root set).
// =============================================================================

func TestGenerateRootfsEndToEnd(t *testing.T) {
	// buildRootfsRoots adds perl-base and mawk plus all Essential pkgs.
	// We need every reachable name in the index.
	fr := newFakeRepo(t, "testing", "amd64", []fakePkg{
		{name: "core-libs", version: "1.0", essential: true},
		{name: "perl-base", version: "5.40"},
		{name: "mawk", version: "1.3"},
	})

	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	outputDir := t.TempDir()
	res, err := GenerateRootfs(RootfsConfig{
		Ctx:       context.Background(),
		Store:     store,
		RepoURL:   fr.srv.URL,
		Dist:      "testing",
		Arch:      "amd64",
		OutputDir: outputDir,
	})
	if err != nil {
		t.Fatalf("GenerateRootfs: %v", err)
	}

	if res.Packages < 3 {
		t.Errorf("expected at least 3 packages (essential + perl-base + mawk), got %d", res.Packages)
	}

	// rootfs-deps.yml must land directly in OutputDir, NOT under pkgs/<name>/.
	rootDepsYML := filepath.Join(outputDir, "rootfs-deps.yml")
	tools, err := buildcfg.LoadBuildDeps(rootDepsYML)
	if err != nil {
		t.Fatalf("load rootfs-deps.yml: %v", err)
	}

	var hasPerl, hasApt bool
	for _, tool := range tools {
		switch tool.Name {
		case "perl-base":
			hasPerl = true
		case "apt":
			hasApt = true
		}
	}
	if !hasPerl {
		t.Errorf("rootfs-deps.yml missing perl-base; tools: %v", tools)
	}
	// apt is intentionally excluded from rootfs roots.
	if hasApt {
		t.Errorf("rootfs-deps.yml unexpectedly contains apt")
	}
}
