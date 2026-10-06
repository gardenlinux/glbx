package restore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gardenlinux/glbx/internal/objstore"
)

func testStore(t *testing.T) *objstore.Store {
	t.Helper()
	s, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func cfgFor(store *objstore.Store, srv *httptest.Server) Config {
	cfg := Config{Ctx: context.Background(), Store: store, HTTPClient: srv.Client()}
	cfg.applyDefaults()
	return cfg
}

func TestFetchItemFirstURLSucceeds(t *testing.T) {
	content := []byte("alpha")
	want := objstore.HashBytes(content)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	store := testStore(t)
	cfg := cfgFor(store, srv)

	fetched, err := fetchItem(cfg, item{hash: want, urls: []string{srv.URL + "/a"}, label: "alpha"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fetched {
		t.Error("expected fetched=true")
	}
	if !store.Blobs.Has(want) {
		t.Error("blob not stored")
	}
}

func TestFetchItemFallsBackOn404(t *testing.T) {
	content := []byte("beta")
	want := objstore.HashBytes(content)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/good") {
			w.Write(content)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	store := testStore(t)
	cfg := cfgFor(store, srv)

	fetched, err := fetchItem(cfg, item{hash: want, urls: []string{srv.URL + "/missing", srv.URL + "/good"}, label: "beta"})
	if err != nil {
		t.Fatalf("second URL should succeed, got error: %v", err)
	}
	if !fetched {
		t.Error("expected fetched=true")
	}
}

func TestFetchItemFallsBackOnHashMismatch(t *testing.T) {
	content := []byte("gamma")
	want := objstore.HashBytes(content)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/good") {
			w.Write(content)
			return
		}
		w.Write([]byte("corrupt")) // wrong bytes -> hash mismatch
	}))
	defer srv.Close()

	store := testStore(t)
	cfg := cfgFor(store, srv)

	fetched, err := fetchItem(cfg, item{hash: want, urls: []string{srv.URL + "/bad", srv.URL + "/good"}, label: "gamma"})
	if err != nil {
		t.Fatalf("second URL should succeed, got error: %v", err)
	}
	if !fetched {
		t.Error("expected fetched=true")
	}
	// The expected blob is present; the mismatched download is left for GC.
	if !store.Blobs.Has(want) {
		t.Error("expected blob not stored")
	}
	if !store.Blobs.Has(objstore.HashBytes([]byte("corrupt"))) {
		t.Error("mismatched blob should be left in place, not cleaned up")
	}
}

func TestFetchItemAllURLsFail(t *testing.T) {
	content := []byte("delta")
	want := objstore.HashBytes(content)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/mismatch") {
			w.Write([]byte("nope"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	store := testStore(t)
	cfg := cfgFor(store, srv)

	u404 := srv.URL + "/missing"
	uMismatch := srv.URL + "/mismatch"
	_, err := fetchItem(cfg, item{hash: want, urls: []string{u404, uMismatch}, label: "delta"})
	if err == nil {
		t.Fatal("expected error when all URLs fail")
	}
	msg := err.Error()
	for _, want := range []string{u404, uMismatch, "HTTP 404", "hash mismatch"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should contain %q", msg, want)
		}
	}
}

func TestFetchItemAlreadyCached(t *testing.T) {
	content := []byte("epsilon")
	store := testStore(t)
	want, err := store.Blobs.Store(strings.NewReader(string(content)))
	if err != nil {
		t.Fatal(err)
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	cfg := cfgFor(store, srv)

	fetched, err := fetchItem(cfg, item{hash: want, urls: []string{srv.URL + "/x"}, label: "epsilon"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetched {
		t.Error("expected fetched=false for a cached blob")
	}
	if hits.Load() != 0 {
		t.Errorf("server hit %d times, expected 0", hits.Load())
	}
}

func TestCollectItemsDedupesAndArchFilters(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"rootfs.yml": "packages: [dpkg]\n",
		"pkgs/foo/sources.yml": `sources:
  - file: foo_1.0.orig.tar.gz
    sha256: ` + hex64('a') + `
    urls: [https://m/foo.tar.gz, https://s/foo.tar.gz]
`,
		"pkgs/foo/build-deps.yml": `tools:
  - name: gcc
    version: "1"
    files:
      - arch: amd64
        sha256: ` + hex64('b') + `
        urls: [https://m/gcc_amd64.deb]
      - arch: arm64
        sha256: ` + hex64('c') + `
        urls: [https://m/gcc_arm64.deb]
`,
		// bar pins the SAME amd64 gcc hash -> must dedupe to one item.
		"pkgs/bar/build-deps.yml": `tools:
  - name: gcc
    version: "1"
    files:
      - arch: amd64
        sha256: ` + hex64('b') + `
        urls: [https://m/gcc_amd64.deb]
`,
		"rootfs-deps.yml": `tools:
  - name: dpkg
    version: "1"
    files:
      - arch: all
        sha256: ` + hex64('d') + `
        urls: [https://m/dpkg_all.deb]
`,
	})

	items, err := collectItems(root, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	// Expect: foo source (a), gcc amd64 (b, deduped), dpkg all (d). NOT arm64 (c).
	got := map[string]bool{}
	for _, it := range items {
		got[it.hash.String()] = true
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(items), items)
	}
	if !got[hex64('a')] || !got[hex64('b')] || !got[hex64('d')] {
		t.Errorf("missing expected hashes: %v", got)
	}
	if got[hex64('c')] {
		t.Error("arm64 file should be filtered out for amd64 target")
	}
}

func TestRestoreEndToEnd(t *testing.T) {
	source := []byte("source-archive-bytes")
	deb := []byte("deb-bytes")
	sourceHash := objstore.HashBytes(source)
	debHash := objstore.HashBytes(deb)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/src"):
			w.Write(source)
		case strings.HasSuffix(r.URL.Path, "/deb"):
			w.Write(deb)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"rootfs.yml": "packages: []\n",
		"pkgs/foo/sources.yml": `sources:
  - file: foo.orig.tar.gz
    sha256: ` + sourceHash.String() + `
    urls: [` + srv.URL + `/src]
`,
		"pkgs/foo/build-deps.yml": `tools:
  - name: tool
    version: "1"
    files:
      - arch: amd64
        sha256: ` + debHash.String() + `
        urls: [` + srv.URL + `/deb]
`,
	})

	store := testStore(t)
	res, err := Restore(Config{Ctx: context.Background(), Store: store, ConfRoot: root, Arch: "amd64", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Restored != 2 || res.Cached != 0 || res.Failed != 0 {
		t.Errorf("result = %+v, want 2 restored", res)
	}
	if !store.Blobs.Has(sourceHash) || !store.Blobs.Has(debHash) {
		t.Error("blobs not populated")
	}

	// A second run finds everything cached.
	res2, err := Restore(Config{Ctx: context.Background(), Store: store, ConfRoot: root, Arch: "amd64", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Cached != 2 || res2.Restored != 0 {
		t.Errorf("second run = %+v, want 2 cached", res2)
	}
}

// hex64 builds a valid 64-char hex SHA-256 string from a single hex digit.
func hex64(c rune) string {
	return strings.Repeat(string(c), 64)
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
