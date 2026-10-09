package importer

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

// multiPkgSourceServer stands up an httptest APT repository under dist whose
// Sources index lists each (name, version) in versions, so a check can resolve
// a newest version per package. Files are minimal — check-updates only reads
// Package/Version/Directory — but a Checksums-Sha256 stanza is required.
func multiPkgSourceServer(t *testing.T, dist string, versions map[string][]string) (string, *http.Client) {
	t.Helper()

	// A stable, syntactically-valid orig-tarball checksum line per stanza.
	zeroHash := fmt.Sprintf("%064d", 0)
	var body string
	names := make([]string, 0, len(versions))
	for n := range versions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, ver := range versions[name] {
			body += fmt.Sprintf("Package: %s\nVersion: %s\nFormat: 3.0 (quilt)\nDirectory: pool/main/%c/%s\nChecksums-Sha256:\n %s 100 %s_%s.orig.tar.gz\n\n",
				name, ver, name[0], name, zeroHash, name, ver)
		}
	}

	sourcesCompressed := createGzipData(t, []byte(body))
	sourcesGzHash := sha256sum(sourcesCompressed)

	releaseBody := fmt.Sprintf(`Origin: Debian
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
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL, server.Client()
}

// TestCheckUpdates covers the highest-vs-pinned comparison, the newest-version
// selection when the index lists several, update-tag filtering, and the
// explicit-package-list intersection.
func TestCheckUpdates(t *testing.T) {
	store := setupTestStore(t)
	root := gitInit(t)

	// Pin three packages on their lineages, all tracking debian:testing.
	importAndMerge(t, root, "foo", "1.0", "")
	importAndMerge(t, root, "bar", "2.0", "")
	importAndMerge(t, root, "baz", "3.0", "")

	// The archive offers: foo newer (several versions, newest 1.3), bar same,
	// baz older (never an update), qux absent from the tree (ignored).
	repoURL, client := multiPkgSourceServer(t, "testing", map[string][]string{
		"foo": {"1.0", "1.3", "1.2"},
		"bar": {"2.0"},
		"baz": {"2.9"},
		"qux": {"9.9"},
	})

	base := CheckConfig{
		Import: ImportConfig{
			Store:      store,
			RepoURL:    repoURL,
			Dist:       "testing",
			NoVerify:   true,
			HTTPClient: client,
		},
		ConfDir: root,
	}

	updates, err := CheckUpdates(base)
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	got := map[string]Update{}
	for _, u := range updates {
		got[u.Pkg] = u
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly one update (foo), got %+v", updates)
	}
	if u := got["foo"]; u.Old != "1.0" || u.New != "1.3" {
		t.Errorf("foo update = %+v, want old 1.0 -> new 1.3 (highest)", u)
	}

	// Explicit package list intersects: asking for baz only yields nothing (baz
	// is not an update), and never surfaces foo.
	only := base
	only.Only = []string{"baz"}
	updates, err = CheckUpdates(only)
	if err != nil {
		t.Fatalf("CheckUpdates(only baz): %v", err)
	}
	if len(updates) != 0 {
		t.Errorf("expected no updates for baz, got %+v", updates)
	}
}

// TestCheckUpdatesTagFilter asserts --update-tag keeps only packages whose
// recorded auto_update matches, so a replay of one series ignores another.
func TestCheckUpdatesTagFilter(t *testing.T) {
	store := setupTestStore(t)
	root := gitInit(t)

	// foo tracks testing, sid-pkg tracks sid. Both have a newer version.
	importAndMergeTag(t, root, "foo", "1.0", "", "debian:testing")
	importAndMergeTag(t, root, "sidpkg", "1.0", "", "debian:sid")

	repoURL, client := multiPkgSourceServer(t, "testing", map[string][]string{
		"foo":    {"1.1"},
		"sidpkg": {"1.1"},
	})

	cfg := CheckConfig{
		Import: ImportConfig{
			Store:      store,
			RepoURL:    repoURL,
			Dist:       "testing",
			NoVerify:   true,
			HTTPClient: client,
		},
		ConfDir:   root,
		UpdateTag: "debian:testing",
	}

	updates, err := CheckUpdates(cfg)
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if len(updates) != 1 || updates[0].Pkg != "foo" {
		t.Fatalf("tag filter should yield only foo, got %+v", updates)
	}
}
