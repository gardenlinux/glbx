package importer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/objstore"
)

// --- Test Helpers ---

// wrapInPGPEnvelope wraps content in a minimal PGP cleartext signature envelope.
// This is needed because ExtractClearSignedPayload() requires the PGP markers
// even when GPG verification is skipped (NoVerify=true).
func wrapInPGPEnvelope(content string) string {
	return "-----BEGIN PGP SIGNED MESSAGE-----\n" +
		"Hash: SHA256\n" +
		"\n" +
		content +
		"-----BEGIN PGP SIGNATURE-----\n" +
		"\n" +
		"fakesignaturedata\n" +
		"-----END PGP SIGNATURE-----\n"
}

// createGzipData compresses data with gzip.
func createGzipData(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sha256sum returns the hex-encoded SHA-256 digest of data.
func sha256sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// createTarGz creates a gzipped tar archive from a map of path -> content.
func createTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if strings.HasSuffix(name, "/") {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(name, "/") {
			if _, err := tw.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// setupTestStore creates a temporary object store for testing.
func setupTestStore(t *testing.T) *objstore.Store {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := objstore.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// --- Tests ---

// Tests for ParseReleaseHashes have moved to internal/debian/aptrepo.

func TestFindSourcePackage(t *testing.T) {
	sourcesData := []byte(`Package: hello
Version: 2.10-3
Format: 3.0 (quilt)
Directory: pool/main/h/hello
Checksums-Sha256:
 6eb2a56bdef5c1f49f03fb365f97e7f5a75c5776de3c9e6b07fa5c52e0e998e2 760166 hello_2.10.orig.tar.gz
 a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2 6052 hello_2.10-3.debian.tar.xz

Package: goodbye
Version: 1.0-1
Format: 3.0 (native)
Directory: pool/main/g/goodbye
Checksums-Sha256:
 deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef 1234 goodbye_1.0.tar.xz
`)

	pkg, err := findSourcePackage(sourcesData, "hello")
	if err != nil {
		t.Fatalf("findSourcePackage failed: %v", err)
	}

	if pkg.Name != "hello" {
		t.Errorf("wrong name: %s", pkg.Name)
	}
	if pkg.Version != "2.10-3" {
		t.Errorf("wrong version: %s", pkg.Version)
	}
	if pkg.Format != "3.0 (quilt)" {
		t.Errorf("wrong format: %s", pkg.Format)
	}
	if pkg.Directory != "pool/main/h/hello" {
		t.Errorf("wrong directory: %s", pkg.Directory)
	}
	if len(pkg.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(pkg.Files))
	}
	if pkg.Files[0].Name != "hello_2.10.orig.tar.gz" {
		t.Errorf("wrong first file name: %s", pkg.Files[0].Name)
	}
	if pkg.Files[1].Name != "hello_2.10-3.debian.tar.xz" {
		t.Errorf("wrong second file name: %s", pkg.Files[1].Name)
	}
}

func TestFindSourcePackageNotFound(t *testing.T) {
	sourcesData := []byte(`Package: hello
Version: 2.10-3
Format: 3.0 (quilt)
Directory: pool/main/h/hello
Checksums-Sha256:
 6eb2a56bdef5c1f49f03fb365f97e7f5a75c5776de3c9e6b07fa5c52e0e998e2 760166 hello_2.10.orig.tar.gz
`)

	_, err := findSourcePackage(sourcesData, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing package")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found': %v", err)
	}
}

func TestFormatDetection(t *testing.T) {
	tests := []struct {
		format     string
		normalized string
	}{
		{"3.0 (quilt)", "3.0 (quilt)"},
		{"3.0 (native)", "3.0 (native)"},
		{"1.0", "1.0"},
		{" 3.0 (quilt) ", "3.0 (quilt)"},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			got := normalizeFormat(tt.format)
			if got != tt.normalized {
				t.Errorf("normalizeFormat(%q) = %q, want %q", tt.format, got, tt.normalized)
			}
		})
	}
}

func TestIsOrigTarball(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"hello_2.10.orig.tar.gz", true},
		{"hello_2.10.orig.tar.xz", true},
		{"hello_2.10.orig-extra.tar.gz", true},
		{"hello_2.10-3.debian.tar.xz", false},
		{"hello_2.10-3.diff.gz", false},
		{"hello_2.10.tar.xz", false},
		// Detached upstream signatures sit next to the orig in some packages
		// (coreutils, gnupg, ...) — they must not be treated as orig tarballs.
		{"coreutils_9.10.orig.tar.xz.asc", false},
		{"foo_1.0.orig.tar.gz.sig", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOrigTarball(tt.name)
			if got != tt.want {
				t.Errorf("isOrigTarball(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestIsDiffFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"hello_2.10-3.diff.gz", true},
		{"hello_2.10-3.diff.xz", true},
		{"hello_2.10-3.diff.bz2", true},
		{"hello_2.10.orig.tar.gz", false},
		{"hello_2.10-3.debian.tar.xz", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDiffFile(tt.name)
			if got != tt.want {
				t.Errorf("isDiffFile(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestSourcesYMLWriteAndParse(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "pkgs", "hello")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hash1 := objstore.MustHash("6eb2a56bdef5c1f49f03fb365f97e7f5a75c5776de3c9e6b07fa5c52e0e998e2")
	hash2 := objstore.MustHash("a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2")

	entries := []SourceEntry{
		{Name: "hello_2.10.orig.tar.gz", Hash: hash1, URLs: []string{"https://deb.debian.org/debian/pool/main/h/hello/hello_2.10.orig.tar.gz"}},
		{Name: "hello_2.10.orig-docs.tar.gz", Hash: hash2},
	}

	if err := writeSourcesYML(pkgDir, entries); err != nil {
		t.Fatalf("writeSourcesYML failed: %v", err)
	}

	// Read back and verify
	data, err := os.ReadFile(filepath.Join(pkgDir, "sources.yml"))
	if err != nil {
		t.Fatalf("reading sources.yml: %v", err)
	}

	if !strings.Contains(string(data), "hello_2.10.orig.tar.gz") {
		t.Error("sources.yml missing first entry name")
	}
	if !strings.Contains(string(data), hash1.String()) {
		t.Error("sources.yml missing first entry hash")
	}
	if !strings.Contains(string(data), "hello_2.10.orig-docs.tar.gz") {
		t.Error("sources.yml missing second entry name")
	}

	// Parse it back
	parsed, err := ParseSourcesYML(data)
	if err != nil {
		t.Fatalf("ParseSourcesYML failed: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(parsed))
	}
	if parsed[0].Name != "hello_2.10.orig.tar.gz" {
		t.Errorf("wrong first name: %s", parsed[0].Name)
	}
	if !parsed[0].Hash.Equal(hash1) {
		t.Errorf("wrong first hash: %s", parsed[0].Hash)
	}
	if len(parsed[0].URLs) != 1 || parsed[0].URLs[0] != "https://deb.debian.org/debian/pool/main/h/hello/hello_2.10.orig.tar.gz" {
		t.Errorf("wrong first urls: %v", parsed[0].URLs)
	}
	if parsed[1].Name != "hello_2.10.orig-docs.tar.gz" {
		t.Errorf("wrong second name: %s", parsed[1].Name)
	}
	if !parsed[1].Hash.Equal(hash2) {
		t.Errorf("wrong second hash: %s", parsed[1].Hash)
	}
}

func TestSourcesYMLEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "pkgs", "native-pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Empty entries should not create a file
	if err := writeSourcesYML(pkgDir, nil); err != nil {
		t.Fatalf("writeSourcesYML failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(pkgDir, "sources.yml")); !os.IsNotExist(err) {
		t.Error("sources.yml should not exist for empty entries")
	}
}

func TestExtractQuilt(t *testing.T) {
	store := setupTestStore(t)

	// Create a minimal .debian.tar.gz containing a debian/ directory
	debianTarContent := createTarGz(t, map[string]string{
		"debian/":          "",
		"debian/control":   "Source: hello\nMaintainer: Test <test@test.org>\n",
		"debian/rules":     "#!/usr/bin/make -f\n%:\n\tdh $@\n",
		"debian/changelog": "hello (2.10-3) unstable; urgency=medium\n\n  * Test\n\n -- Test <test@test.org>  Mon, 01 Jan 2024 00:00:00 +0000\n",
	})

	// Store the debian tarball in the object store
	debTarHash, err := store.Blobs.Store(bytes.NewReader(debianTarContent))
	if err != nil {
		t.Fatalf("storing debian tarball: %v", err)
	}

	// Also create and store an orig tarball (not needed for extraction but for completeness)
	origContent := createTarGz(t, map[string]string{
		"hello-2.10/":         "",
		"hello-2.10/hello.c":  "int main() { return 0; }\n",
		"hello-2.10/Makefile": "all: hello\n",
	})
	origHash, err := store.Blobs.Store(bytes.NewReader(origContent))
	if err != nil {
		t.Fatalf("storing orig tarball: %v", err)
	}

	pkg := &sourcePackage{
		Name:      "hello",
		Version:   "2.10-3",
		Format:    "3.0 (quilt)",
		Directory: "pool/main/h/hello",
		Files: []sourceFile{
			{Hash: origHash.String(), Size: fmt.Sprintf("%d", len(origContent)), Name: "hello_2.10.orig.tar.gz"},
			{Hash: debTarHash.String(), Size: fmt.Sprintf("%d", len(debianTarContent)), Name: "hello_2.10-3.debian.tar.gz"},
		},
	}

	files := map[string]objstore.Hash{
		"hello_2.10.orig.tar.gz":     origHash,
		"hello_2.10-3.debian.tar.gz": debTarHash,
	}

	pkgDir := t.TempDir()
	cfg := ImportConfig{Store: store}

	if err := extractQuilt(cfg, pkg, files, pkgDir); err != nil {
		t.Fatalf("extractQuilt failed: %v", err)
	}

	// Verify debian/control exists
	controlPath := filepath.Join(pkgDir, "src", "debian", "control")
	data, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatalf("reading debian/control: %v", err)
	}
	if !strings.Contains(string(data), "Source: hello") {
		t.Errorf("debian/control has wrong content: %s", string(data))
	}

	// Verify debian/rules exists
	rulesPath := filepath.Join(pkgDir, "src", "debian", "rules")
	if _, err := os.Stat(rulesPath); os.IsNotExist(err) {
		t.Error("debian/rules not found")
	}
}

func TestExtractNative(t *testing.T) {
	store := setupTestStore(t)

	// Create a native tarball (contains the full source with debian/)
	nativeTarContent := createTarGz(t, map[string]string{
		"hello-1.0/":                 "",
		"hello-1.0/hello.c":          "int main() { return 0; }\n",
		"hello-1.0/Makefile":         "all: hello\n",
		"hello-1.0/debian/":          "",
		"hello-1.0/debian/control":   "Source: hello\nMaintainer: Test <test@test.org>\n",
		"hello-1.0/debian/rules":     "#!/usr/bin/make -f\n%:\n\tdh $@\n",
		"hello-1.0/debian/changelog": "hello (1.0) unstable; urgency=medium\n\n  * Native package\n\n -- Test <test@test.org>  Mon, 01 Jan 2024 00:00:00 +0000\n",
	})

	nativeHash, err := store.Blobs.Store(bytes.NewReader(nativeTarContent))
	if err != nil {
		t.Fatalf("storing native tarball: %v", err)
	}

	pkg := &sourcePackage{
		Name:      "hello",
		Version:   "1.0",
		Format:    "3.0 (native)",
		Directory: "pool/main/h/hello",
		Files: []sourceFile{
			{Hash: nativeHash.String(), Size: fmt.Sprintf("%d", len(nativeTarContent)), Name: "hello_1.0.tar.gz"},
		},
	}

	files := map[string]objstore.Hash{
		"hello_1.0.tar.gz": nativeHash,
	}

	pkgDir := t.TempDir()
	cfg := ImportConfig{Store: store}

	if err := extractNative(cfg, pkg, files, pkgDir); err != nil {
		t.Fatalf("extractNative failed: %v", err)
	}

	// Verify debian/control exists
	controlPath := filepath.Join(pkgDir, "src", "debian", "control")
	data, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatalf("reading debian/control: %v", err)
	}
	if !strings.Contains(string(data), "Source: hello") {
		t.Errorf("debian/control has wrong content: %s", string(data))
	}

	// Verify that source files ARE in the output for native packages.
	// Per the architecture blueprint: "Native package handling — 3.0 (native)
	// format packages store their entire source tree in pkgDir/src/ (not just
	// debian/). The importer extracts the full tarball."
	helloC := filepath.Join(pkgDir, "src", "hello.c")
	if _, err := os.Stat(helloC); os.IsNotExist(err) {
		t.Error("hello.c SHOULD be in the output for native packages (full source tree)")
	}
}

func TestParseFileList(t *testing.T) {
	field := `
 6eb2a56bdef5c1f49f03fb365f97e7f5a75c5776de3c9e6b07fa5c52e0e998e2 760166 hello_2.10.orig.tar.gz
 a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2 6052 hello_2.10-3.debian.tar.xz`

	files, err := parseFileList(field)
	if err != nil {
		t.Fatalf("parseFileList failed: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	if files[0].Hash != "6eb2a56bdef5c1f49f03fb365f97e7f5a75c5776de3c9e6b07fa5c52e0e998e2" {
		t.Errorf("wrong hash for first file: %s", files[0].Hash)
	}
	if files[0].Size != "760166" {
		t.Errorf("wrong size for first file: %s", files[0].Size)
	}
	if files[0].Name != "hello_2.10.orig.tar.gz" {
		t.Errorf("wrong name for first file: %s", files[0].Name)
	}
}

func TestImportFullE2E(t *testing.T) {
	store := setupTestStore(t)

	// Create test source package files
	origContent := createTarGz(t, map[string]string{
		"hello-2.10/":         "",
		"hello-2.10/hello.c":  "int main() { return 0; }\n",
		"hello-2.10/Makefile": "all: hello\n",
	})
	origHash := sha256sum(origContent)

	debianTarContent := createTarGz(t, map[string]string{
		"debian/":          "",
		"debian/control":   "Source: hello\nMaintainer: Test <test@test.org>\n",
		"debian/rules":     "#!/usr/bin/make -f\n%:\n\tdh $@\n",
		"debian/changelog": "hello (2.10-3) unstable; urgency=medium\n\n  * Test\n\n -- Test <test@test.org>  Mon, 01 Jan 2024 00:00:00 +0000\n",
	})
	debianTarHash := sha256sum(debianTarContent)

	// Create Sources index
	sourcesContent := fmt.Sprintf(`Package: hello
Version: 2.10-3
Format: 3.0 (quilt)
Directory: pool/main/h/hello
Checksums-Sha256:
 %s %d hello_2.10.orig.tar.gz
 %s %d hello_2.10-3.debian.tar.gz
`, origHash, len(origContent), debianTarHash, len(debianTarContent))

	sourcesCompressed := createGzipData(t, []byte(sourcesContent))
	sourcesGzHash := sha256sum(sourcesCompressed)

	// Create Release file wrapped in PGP cleartext signature envelope.
	// Even with NoVerify=true, ExtractClearSignedPayload() is called to strip
	// the envelope, so the test data must include the PGP framing.
	releaseBody := fmt.Sprintf(`Origin: Debian
Label: Debian
Suite: testing
SHA256:
 %s %d main/source/Sources.gz
`, sourcesGzHash, len(sourcesCompressed))
	releaseContent := wrapInPGPEnvelope(releaseBody)

	// Set up mock HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/dists/testing/InRelease", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(releaseContent))
	})
	mux.HandleFunc("/dists/testing/main/source/Sources.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(sourcesCompressed)
	})
	mux.HandleFunc("/pool/main/h/hello/hello_2.10.orig.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(origContent)
	})
	mux.HandleFunc("/pool/main/h/hello/hello_2.10-3.debian.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(debianTarContent)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	outputDir := t.TempDir()

	cfg := ImportConfig{
		Store:      store,
		RepoURL:    server.URL,
		Dist:       "testing",
		NoVerify:   true,
		OutputDir:  outputDir,
		HTTPClient: server.Client(),
	}

	result, err := Import(cfg, "hello")
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Verify result
	if result.Name != "hello" {
		t.Errorf("wrong name: %s", result.Name)
	}
	if result.Version != "2.10-3" {
		t.Errorf("wrong version: %s", result.Version)
	}
	if result.Format != "3.0 (quilt)" {
		t.Errorf("wrong format: %s", result.Format)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("expected 1 source entry, got %d", len(result.Sources))
	}
	if result.Sources[0].Name != "hello_2.10.orig.tar.gz" {
		t.Errorf("wrong source entry name: %s", result.Sources[0].Name)
	}

	// Verify files on disk
	controlPath := filepath.Join(outputDir, "pkgs", "hello", "src", "debian", "control")
	data, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatalf("reading debian/control: %v", err)
	}
	if !strings.Contains(string(data), "Source: hello") {
		t.Errorf("debian/control wrong content: %s", string(data))
	}

	// Verify sources.yml
	sourcesYML := filepath.Join(outputDir, "pkgs", "hello", "sources.yml")
	ymlData, err := os.ReadFile(sourcesYML)
	if err != nil {
		t.Fatalf("reading sources.yml: %v", err)
	}
	if !strings.Contains(string(ymlData), "hello_2.10.orig.tar.gz") {
		t.Errorf("sources.yml missing orig tarball name")
	}
	if !strings.Contains(string(ymlData), origHash) {
		t.Errorf("sources.yml missing orig tarball hash")
	}

	// Verify blob is in store
	h, _ := objstore.NewHash(origHash)
	if !store.Blobs.Has(h) {
		t.Error("orig tarball blob not found in store")
	}

	// sources.yml records the retrieval URL.
	if !strings.Contains(string(ymlData), "pool/main/h/hello/hello_2.10.orig.tar.gz") {
		t.Errorf("sources.yml missing retrieval url")
	}
}

func TestImportNativeE2E(t *testing.T) {
	store := setupTestStore(t)

	// Create a native source package tarball
	nativeContent := createTarGz(t, map[string]string{
		"debconf-1.5/":                 "",
		"debconf-1.5/debconf":          "#!/usr/bin/perl\nprint 'hello';\n",
		"debconf-1.5/debian/":          "",
		"debconf-1.5/debian/control":   "Source: debconf\nMaintainer: Test <test@test.org>\n",
		"debconf-1.5/debian/rules":     "#!/usr/bin/make -f\n%:\n\tdh $@\n",
		"debconf-1.5/debian/changelog": "debconf (1.5) unstable; urgency=medium\n\n  * Native\n\n -- Test <test@test.org>  Mon, 01 Jan 2024 00:00:00 +0000\n",
	})
	nativeHash := sha256sum(nativeContent)

	sourcesContent := fmt.Sprintf(`Package: debconf
Version: 1.5
Format: 3.0 (native)
Directory: pool/main/d/debconf
Checksums-Sha256:
 %s %d debconf_1.5.tar.gz
`, nativeHash, len(nativeContent))

	sourcesCompressed := createGzipData(t, []byte(sourcesContent))
	sourcesGzHash := sha256sum(sourcesCompressed)

	releaseBody := fmt.Sprintf(`Origin: Debian
Label: Debian
Suite: testing
SHA256:
 %s %d main/source/Sources.gz
`, sourcesGzHash, len(sourcesCompressed))
	releaseContent := wrapInPGPEnvelope(releaseBody)

	mux := http.NewServeMux()
	mux.HandleFunc("/dists/testing/InRelease", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(releaseContent))
	})
	mux.HandleFunc("/dists/testing/main/source/Sources.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(sourcesCompressed)
	})
	mux.HandleFunc("/pool/main/d/debconf/debconf_1.5.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(nativeContent)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	outputDir := t.TempDir()

	cfg := ImportConfig{
		Store:      store,
		RepoURL:    server.URL,
		Dist:       "testing",
		NoVerify:   true,
		OutputDir:  outputDir,
		HTTPClient: server.Client(),
	}

	result, err := Import(cfg, "debconf")
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if result.Name != "debconf" {
		t.Errorf("wrong name: %s", result.Name)
	}
	if result.Version != "1.5" {
		t.Errorf("wrong version: %s", result.Version)
	}
	if result.Format != "3.0 (native)" {
		t.Errorf("wrong format: %s", result.Format)
	}
	if len(result.Sources) != 0 {
		t.Errorf("native packages should have no orig sources, got %d", len(result.Sources))
	}

	// Verify debian/control
	controlPath := filepath.Join(outputDir, "pkgs", "debconf", "src", "debian", "control")
	data, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatalf("reading debian/control: %v", err)
	}
	if !strings.Contains(string(data), "Source: debconf") {
		t.Errorf("wrong debian/control content: %s", string(data))
	}

	// Verify no sources.yml (native package has no orig tarballs)
	sourcesYML := filepath.Join(outputDir, "pkgs", "debconf", "sources.yml")
	if _, err := os.Stat(sourcesYML); !os.IsNotExist(err) {
		t.Error("native package should not have sources.yml")
	}
}

func TestImportMissingPackage(t *testing.T) {
	store := setupTestStore(t)

	sourcesContent := `Package: other
Version: 1.0-1
Format: 3.0 (quilt)
Directory: pool/main/o/other
Checksums-Sha256:
 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 1234 other_1.0.orig.tar.gz
`
	sourcesCompressed := createGzipData(t, []byte(sourcesContent))
	sourcesGzHash := sha256sum(sourcesCompressed)

	releaseBody := fmt.Sprintf(`Origin: Debian
Suite: testing
SHA256:
 %s %d main/source/Sources.gz
`, sourcesGzHash, len(sourcesCompressed))
	releaseContent := wrapInPGPEnvelope(releaseBody)

	mux := http.NewServeMux()
	mux.HandleFunc("/dists/testing/InRelease", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(releaseContent))
	})
	mux.HandleFunc("/dists/testing/main/source/Sources.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(sourcesCompressed)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := ImportConfig{
		Store:      store,
		RepoURL:    server.URL,
		Dist:       "testing",
		NoVerify:   true,
		OutputDir:  t.TempDir(),
		HTTPClient: server.Client(),
	}

	_, err := Import(cfg, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing package")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found': %v", err)
	}
}

func TestImportHashMismatch(t *testing.T) {
	store := setupTestStore(t)

	sourcesCompressed := createGzipData(t, []byte("Package: test\n"))
	// Use wrong hash in Release file
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"

	releaseBody := fmt.Sprintf(`Origin: Debian
Suite: testing
SHA256:
 %s %d main/source/Sources.gz
`, wrongHash, len(sourcesCompressed))
	releaseContent := wrapInPGPEnvelope(releaseBody)

	mux := http.NewServeMux()
	mux.HandleFunc("/dists/testing/InRelease", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(releaseContent))
	})
	mux.HandleFunc("/dists/testing/main/source/Sources.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(sourcesCompressed)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := ImportConfig{
		Store:      store,
		RepoURL:    server.URL,
		Dist:       "testing",
		NoVerify:   true,
		OutputDir:  t.TempDir(),
		HTTPClient: server.Client(),
	}

	_, err := Import(cfg, "test")
	if err == nil {
		t.Fatal("expected error for hash mismatch")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("error should mention 'hash mismatch': %v", err)
	}
}

func TestImportRequiredFields(t *testing.T) {
	store := setupTestStore(t)

	tests := []struct {
		name string
		cfg  ImportConfig
		pkg  string
		want string
	}{
		{
			name: "missing store",
			cfg:  ImportConfig{OutputDir: "/tmp/test"},
			pkg:  "hello",
			want: "Store is required",
		},
		{
			name: "missing output dir",
			cfg:  ImportConfig{Store: store},
			pkg:  "hello",
			want: "OutputDir is required",
		},
		{
			name: "missing package name",
			cfg:  ImportConfig{Store: store, OutputDir: "/tmp/test"},
			pkg:  "",
			want: "package name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Import(tt.cfg, tt.pkg)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error should contain %q: %v", tt.want, err)
			}
		})
	}
}

func TestFilterOrigEntries(t *testing.T) {
	hash1 := objstore.MustHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	hash2 := objstore.MustHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	hash3 := objstore.MustHash("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")

	pkg := &sourcePackage{
		Directory: "pool/main/h/hello",
		Files: []sourceFile{
			{Name: "hello_2.10.orig.tar.gz", Hash: hash1.String()},
			{Name: "hello_2.10-3.debian.tar.xz", Hash: hash2.String()},
			{Name: "hello_2.10.orig-extra.tar.gz", Hash: hash3.String()},
		},
	}

	files := map[string]objstore.Hash{
		"hello_2.10.orig.tar.gz":       hash1,
		"hello_2.10-3.debian.tar.xz":   hash2,
		"hello_2.10.orig-extra.tar.gz": hash3,
	}

	entries := filterOrigEntries(ImportConfig{RepoURL: "https://deb.debian.org/debian"}, pkg, files)
	if len(entries) != 2 {
		t.Fatalf("expected 2 orig entries, got %d", len(entries))
	}
	if entries[0].Name != "hello_2.10.orig.tar.gz" {
		t.Errorf("wrong first entry: %s", entries[0].Name)
	}
	if len(entries[0].URLs) != 1 || entries[0].URLs[0] != "https://deb.debian.org/debian/pool/main/h/hello/hello_2.10.orig.tar.gz" {
		t.Errorf("wrong first entry url: %v", entries[0].URLs)
	}
	if entries[1].Name != "hello_2.10.orig-extra.tar.gz" {
		t.Errorf("wrong second entry: %s", entries[1].Name)
	}
}

func TestCopyDir(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "copy")

	// Create a directory structure
	os.MkdirAll(filepath.Join(srcDir, "subdir"), 0o755)
	os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("content1"), 0o644)
	os.WriteFile(filepath.Join(srcDir, "subdir", "file2.txt"), []byte("content2"), 0o644)

	if err := copyDir(srcDir, dstDir); err != nil {
		t.Fatalf("copyDir failed: %v", err)
	}

	// Verify
	data, err := os.ReadFile(filepath.Join(dstDir, "file1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "content1" {
		t.Errorf("wrong content: %s", string(data))
	}

	data, err = os.ReadFile(filepath.Join(dstDir, "subdir", "file2.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "content2" {
		t.Errorf("wrong content: %s", string(data))
	}
}

func TestHTTPErrors(t *testing.T) {
	store := setupTestStore(t)

	// Server that returns 404
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	cfg := ImportConfig{
		Store:      store,
		RepoURL:    server.URL,
		Dist:       "testing",
		NoVerify:   true,
		OutputDir:  t.TempDir(),
		HTTPClient: server.Client(),
	}

	_, err := Import(cfg, "hello")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404 status: %v", err)
	}
}

// Ensure we handle the case where the download file hash doesn't match
func TestDownloadSourceFilesHashMismatch(t *testing.T) {
	store := setupTestStore(t)

	origContent := []byte("some content")

	mux := http.NewServeMux()
	mux.HandleFunc("/pool/main/h/hello/hello_1.0.orig.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(origContent)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	pkg := &sourcePackage{
		Name:      "hello",
		Version:   "1.0-1",
		Directory: "pool/main/h/hello",
		Files: []sourceFile{
			{
				Hash: "0000000000000000000000000000000000000000000000000000000000000000",
				Size: "12",
				Name: "hello_1.0.orig.tar.gz",
			},
		},
	}

	cfg := ImportConfig{
		Store:      store,
		RepoURL:    server.URL,
		HTTPClient: server.Client(),
	}

	_, err := downloadSourceFiles(cfg, pkg)
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("error should mention hash mismatch: %v", err)
	}
}

// Test findDebianTar, findNativeTar, findOrigTar, findDiffFile helpers
func TestFileFinders(t *testing.T) {
	pkg := &sourcePackage{
		Files: []sourceFile{
			{Name: "hello_2.10.orig.tar.xz"},
			{Name: "hello_2.10-3.debian.tar.xz"},
		},
	}

	if got := findDebianTar(pkg); got != "hello_2.10-3.debian.tar.xz" {
		t.Errorf("findDebianTar: got %q", got)
	}
	if got := findOrigTar(pkg); got != "hello_2.10.orig.tar.xz" {
		t.Errorf("findOrigTar: got %q", got)
	}
	if got := findDiffFile(pkg); got != "" {
		t.Errorf("findDiffFile: got %q, want empty", got)
	}

	nativePkg := &sourcePackage{
		Files: []sourceFile{
			{Name: "hello_1.0.tar.gz"},
		},
	}
	if got := findNativeTar(nativePkg); got != "hello_1.0.tar.gz" {
		t.Errorf("findNativeTar: got %q", got)
	}

	diffPkg := &sourcePackage{
		Files: []sourceFile{
			{Name: "hello_1.0.orig.tar.gz"},
			{Name: "hello_1.0-1.diff.gz"},
		},
	}
	if got := findDiffFile(diffPkg); got != "hello_1.0-1.diff.gz" {
		t.Errorf("findDiffFile: got %q", got)
	}
}
