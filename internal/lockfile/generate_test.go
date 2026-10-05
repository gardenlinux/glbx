package lockfile

import (
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
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/objstore"
)

func TestWriteBuildDeps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "build-deps.yml")

	pkgs := []*index.Package{
		{Name: "gcc-14", Version: "14.2.0-3", Architecture: "amd64", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Filename: "pool/main/g/gcc-14/gcc-14_amd64.deb"},
		{Name: "debhelper", Version: "13.20", Architecture: "all", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Filename: "pool/main/d/debhelper/debhelper_all.deb"},
	}
	if err := writeBuildDeps(path, "https://deb.debian.org/debian", pkgs); err != nil {
		t.Fatal(err)
	}

	tools, err := buildcfg.LoadBuildDeps(path)
	if err != nil {
		t.Fatalf("load back: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name != "gcc-14" || tools[0].Version != "14.2.0-3" {
		t.Errorf("tool[0] = %+v", tools[0])
	}
	if len(tools[0].Files) != 1 || tools[0].Files[0].Arch != "amd64" {
		t.Errorf("tool[0] files = %+v", tools[0].Files)
	}
	if got := tools[0].Files[0].URLs; len(got) != 1 || got[0] != "https://deb.debian.org/debian/pool/main/g/gcc-14/gcc-14_amd64.deb" {
		t.Errorf("tool[0] urls = %v", got)
	}
}

func TestWriteBuildDepsRejectsMissingHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build-deps.yml")
	pkgs := []*index.Package{{Name: "x", Version: "1", Architecture: "amd64", Filename: "pool/x.deb"}}
	if err := writeBuildDeps(path, "https://example", pkgs); err == nil {
		t.Fatal("expected error for package with no SHA256")
	}
}

func TestExtractBuildDeps(t *testing.T) {
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	writeFile(t, controlPath, `Source: mypackage
Section: libs
Build-Depends: debhelper (>= 13), libfoo-dev, pkg-config
Build-Depends-Arch: libc6-dev

Package: mypackage
Architecture: any
`)
	deps, err := extractBuildDeps(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) < 3 {
		t.Fatalf("expected at least 3 deps, got %d", len(deps))
	}
}

func TestExtractBuildDepsMergesArchAndIndep(t *testing.T) {
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	writeFile(t, controlPath, `Source: mypackage
Build-Depends: a
Build-Depends-Arch: b
Build-Depends-Indep: c

Package: mypackage
Architecture: any
`)
	deps, err := extractBuildDeps(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 3 {
		t.Fatalf("expected 3 alternatives (a,b,c), got %d: %#v", len(deps), deps)
	}
}

func TestExtractBuildDepsMissingFile(t *testing.T) {
	if _, err := extractBuildDeps("/nonexistent/control"); err == nil {
		t.Fatal("expected error for missing control file")
	}
}

func TestExtractBuildDepsNoFields(t *testing.T) {
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	writeFile(t, controlPath, `Source: bare
Section: libs

Package: bare
Architecture: any
`)
	deps, err := extractBuildDeps(controlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("expected no deps, got %d", len(deps))
	}
}

func TestConfigApplyDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.RepoURL != "https://deb.debian.org/debian" {
		t.Errorf("RepoURL: %q", cfg.RepoURL)
	}
	if cfg.Dist != "testing" {
		t.Errorf("Dist: %q", cfg.Dist)
	}
	if cfg.Arch == "" {
		t.Errorf("Arch should be detected, got empty")
	}
	if cfg.Ctx == nil {
		t.Errorf("Ctx should default to background")
	}
}

func TestConfigApplyDefaultsRespectsExplicit(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "x")
	cfg := &Config{RepoURL: "http://mirror.local/debian", Dist: "bookworm", Arch: "arm64", Ctx: ctx}
	cfg.applyDefaults()
	if cfg.RepoURL != "http://mirror.local/debian" || cfg.Dist != "bookworm" || cfg.Arch != "arm64" || cfg.Ctx != ctx {
		t.Errorf("explicit values overwritten: %+v", cfg)
	}
}

func TestRootfsConfigApplyDefaults(t *testing.T) {
	cfg := &RootfsConfig{}
	cfg.applyDefaults()
	if cfg.RepoURL == "" || cfg.Dist == "" || cfg.Arch == "" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
}

func TestBuildRootfsRootsIncludesPostinstInfra(t *testing.T) {
	idx := makeIndex(t, []map[string]string{
		{"package": "libc6", "version": "1", "essential": "yes"},
		{"package": "dash", "version": "1"},
	})
	roots := buildRootfsRoots(idx)
	names := make(map[string]bool)
	for _, r := range roots {
		names[r.Name] = true
	}
	for _, want := range []string{"libc6", "perl-base", "mawk"} {
		if !names[want] {
			t.Errorf("buildRootfsRoots missing %q: %v", want, names)
		}
	}
	if names["apt"] {
		t.Errorf("buildRootfsRoots should not include apt — image configuration is dpkg-only")
	}
}

func TestBuildResolverRootsIncludesEssentialAndImplicit(t *testing.T) {
	idx := makeIndex(t, []map[string]string{
		{"package": "libc6", "version": "1", "essential": "yes"},
	})
	roots := buildResolverRoots(nil, idx, "amd64", nil)
	names := make(map[string]bool)
	for _, r := range roots {
		names[r.Name] = true
	}
	for _, want := range []string{"libc6", "build-essential", "fakeroot", "debconf"} {
		if !names[want] {
			t.Errorf("missing %q in roots: %v", want, names)
		}
	}
}

func TestFetchDebsHappyPath(t *testing.T) {
	pkgs, srv := serveFakeDebs(t, map[string][]byte{
		"pool/a.deb": []byte("aaa"),
		"pool/b.deb": []byte("bbb"),
		"pool/c.deb": []byte("ccc"),
	})
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := FetchDebs(context.Background(), store, srv.URL, pkgs); err != nil {
		t.Fatalf("FetchDebs: %v", err)
	}
	for _, p := range pkgs {
		h, _ := objstore.NewHash(p.SHA256)
		if !store.Blobs.Has(h) {
			t.Errorf("blob missing for %s", p.Name)
		}
	}
}

func TestFetchDebsSkipsAlreadyCached(t *testing.T) {
	pkgs, srv := serveFakeDebs(t, map[string][]byte{"pool/a.deb": []byte("preexisting-content")})
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Blobs.Store(strings.NewReader("preexisting-content")); err != nil {
		t.Fatal(err)
	}

	hits := atomic.Int32{}
	wrappedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer wrappedSrv.Close()

	if err := FetchDebs(context.Background(), store, wrappedSrv.URL, pkgs); err != nil {
		t.Fatalf("FetchDebs: %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("server hit %d times, expected 0 (already cached)", hits.Load())
	}
}

func TestFetchDebsHashMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("wrong-content"))
	}))
	defer srv.Close()

	pkgs := []*index.Package{
		{Name: "evil", Filename: "pool/e.deb", SHA256: sha256Hex([]byte("expected-content"))},
	}
	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = FetchDebs(context.Background(), store, srv.URL, pkgs)
	if err == nil {
		t.Fatal("expected hash-mismatch error")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("error should mention hash mismatch, got: %v", err)
	}
	wrongHash, _ := objstore.NewHash(sha256Hex([]byte("wrong-content")))
	if store.Blobs.Has(wrongHash) {
		t.Errorf("FetchDebs stored mismatched content — security regression")
	}
}

func TestFetchDebsHTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	pkgs := []*index.Package{{Name: "missing", Filename: "pool/m.deb", SHA256: sha256Hex([]byte("x"))}}
	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := FetchDebs(context.Background(), store, srv.URL, pkgs); err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestFetchDebsSkipsPackageWithEmptySHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server hit for SHA-less package — should have been skipped")
	}))
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := FetchDebs(context.Background(), store, srv.URL, []*index.Package{{Name: "virtual", SHA256: ""}}); err != nil {
		t.Fatalf("FetchDebs should not error on empty SHA: %v", err)
	}
}

func TestFetchDebsConcurrentBatch(t *testing.T) {
	contentByPath := make(map[string][]byte, 50)
	for i := 0; i < 50; i++ {
		contentByPath[fmt.Sprintf("pool/p%02d.deb", i)] = []byte(fmt.Sprintf("content-%d", i))
	}
	pkgs, srv := serveFakeDebs(t, contentByPath)
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := FetchDebs(context.Background(), store, srv.URL, pkgs); err != nil {
		t.Fatalf("FetchDebs: %v", err)
	}
	for _, p := range pkgs {
		h, _ := objstore.NewHash(p.SHA256)
		if !store.Blobs.Has(h) {
			t.Errorf("blob missing for %s", p.Name)
		}
	}
}

func makeIndex(t *testing.T, stanzas []map[string]string) *index.Index {
	t.Helper()
	var b strings.Builder
	for _, s := range stanzas {
		fmt.Fprintf(&b, "Package: %s\n", s["package"])
		for k, v := range s {
			if k == "package" {
				continue
			}
			fmt.Fprintf(&b, "%s: %s\n", capitalize(k), v)
		}
		b.WriteString("\n")
	}
	idx, err := index.Load(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("makeIndex: %v", err)
	}
	return idx
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// serveFakeDebs serves pathToContent over httptest and returns matching
// index.Package entries with correct SHA256 hashes.
func serveFakeDebs(t *testing.T, pathToContent map[string][]byte) ([]*index.Package, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		content, ok := pathToContent[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(content)
	}))
	var pkgs []*index.Package
	i := 0
	for path, content := range pathToContent {
		pkgs = append(pkgs, &index.Package{
			Name:     fmt.Sprintf("pkg%d", i),
			Filename: path,
			SHA256:   sha256Hex(content),
		})
		i++
	}
	return pkgs, srv
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
