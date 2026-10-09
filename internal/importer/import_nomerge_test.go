package importer

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helloFixtureServer stands up an httptest APT repository under dist, serving a
// single source package (hello 2.10-3, 3.0 quilt), and returns its base URL and
// client. It mirrors the fixtures used by TestImportFullE2E, isolated here so
// the git-lineage import tests can exercise the real HTTP + extract path.
func helloFixtureServer(t *testing.T, dist string) (string, *http.Client) {
	t.Helper()

	origContent := createTarGz(t, map[string]string{
		"hello-2.10/":        "",
		"hello-2.10/hello.c": "int main() { return 0; }\n",
	})
	origHash := sha256sum(origContent)

	debianTarContent := createTarGz(t, map[string]string{
		"debian/":          "",
		"debian/control":   "Source: hello\nMaintainer: Test <test@test.org>\n",
		"debian/rules":     "#!/usr/bin/make -f\n%:\n\tdh $@\n",
		"debian/changelog": "hello (2.10-3) unstable; urgency=medium\n\n  * Test\n\n -- Test <test@test.org>  Mon, 01 Jan 2024 00:00:00 +0000\n",
	})
	debianTarHash := sha256sum(debianTarContent)

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

	releaseBody := fmt.Sprintf(`Origin: Debian
Label: Debian
Suite: %s
SHA256:
 %s %d main/source/Sources.gz
`, dist, sourcesGzHash, len(sourcesCompressed))
	releaseContent := wrapInPGPEnvelope(releaseBody)

	mux := http.NewServeMux()
	mux.HandleFunc("/dists/"+dist+"/InRelease", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(releaseContent))
	})
	mux.HandleFunc("/dists/"+dist+"/main/source/Sources.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(sourcesCompressed)
	})
	mux.HandleFunc("/pool/main/h/hello/hello_2.10.orig.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(origContent)
	})
	mux.HandleFunc("/pool/main/h/hello/hello_2.10-3.debian.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(debianTarContent)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL, server.Client()
}

// TestImportGitModeCommitOnly covers the behaviour the import --no-merge CLI
// surface relies on: a git-mode import builds the pristine import commit U and
// returns its hash without merging. HEAD, the index, and the working tree stay
// untouched, so pkgs/ does not appear until the caller merges U. The import
// commit records the explicit UpdateTag, decoupled from the dist pulled from.
func TestImportGitModeCommitOnly(t *testing.T) {
	store := setupTestStore(t)
	repoURL, client := helloFixtureServer(t, "sid")

	root := gitInit(t)
	headBefore := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	result, err := Import(ImportConfig{
		Store:      store,
		RepoURL:    repoURL,
		Dist:       "sid",
		UpdateTag:  "debian:testing",
		NoVerify:   true,
		OutputDir:  root,
		GitHistory: true,
		HTTPClient: client,
	}, "hello")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if !result.Committed || result.CommitHash == "" {
		t.Fatalf("expected a committed import, got %+v", result)
	}
	if !result.FirstImport {
		t.Error("first import of a package should be flagged FirstImport")
	}
	if result.Version != "2.10-3" {
		t.Errorf("version = %q, want 2.10-3", result.Version)
	}

	// The import must not have merged: HEAD, the index, and the working tree are
	// untouched, and pkgs/ does not exist until the caller merges U.
	if headAfter := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Errorf("HEAD moved: %s -> %s", headBefore, headAfter)
	}
	if status := runGit(t, root, "status", "--porcelain"); status != "" {
		t.Errorf("working tree/index dirtied: %q", status)
	}
	if _, err := os.Stat(filepath.Join(root, "pkgs")); !os.IsNotExist(err) {
		t.Errorf("pkgs/ should not exist until U is merged")
	}

	// The commit records the explicit update tag, not debian:<dist>.
	msg, err := commitMessage(root, result.CommitHash)
	if err != nil {
		t.Fatal(err)
	}
	fields, ok, err := parseImportMetadata(msg)
	if err != nil || !ok {
		t.Fatalf("import metadata parse: ok=%v err=%v", ok, err)
	}
	if fields["auto_update"] != "debian:testing" {
		t.Errorf("auto_update = %q, want debian:testing (decoupled from dist=sid)", fields["auto_update"])
	}
}
