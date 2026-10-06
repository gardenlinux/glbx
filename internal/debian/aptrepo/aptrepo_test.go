package aptrepo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/objstore"
)

func TestParseReleaseHashes(t *testing.T) {
	payload := []byte(`Origin: Debian
Label: Debian
Suite: testing
Codename: trixie
SHA256:
 abc123def456abc123def456abc123def456abc123def456abc123def456abcd 12345 main/source/Sources.gz
 def456abc123def456abc123def456abc123def456abc123def456abc123defg 67890 main/binary-amd64/Packages.gz
`)

	hashes, err := ParseReleaseHashes(payload)
	if err != nil {
		t.Fatalf("ParseReleaseHashes failed: %v", err)
	}

	if got := hashes["main/source/Sources.gz"]; got != "abc123def456abc123def456abc123def456abc123def456abc123def456abcd" {
		t.Errorf("wrong hash for Sources.gz: %s", got)
	}
	if got := hashes["main/binary-amd64/Packages.gz"]; got != "def456abc123def456abc123def456abc123def456abc123def456abc123defg" {
		t.Errorf("wrong hash for Packages.gz: %s", got)
	}
	if len(hashes) != 2 {
		t.Errorf("expected 2 hashes, got %d", len(hashes))
	}
}

func TestParseReleaseHashesMissingSHA256(t *testing.T) {
	payload := []byte(`Origin: Debian
Label: Debian
Suite: testing
MD5Sum:
 d41d8cd98f00b204e9800998ecf8427e 0 main/source/Sources.gz
`)

	_, err := ParseReleaseHashes(payload)
	if err == nil {
		t.Fatal("expected error for missing SHA256 field")
	}
	if !strings.Contains(err.Error(), "SHA256") {
		t.Errorf("error should mention SHA256: %v", err)
	}
}

func TestParseReleaseHashesEmptySHA256(t *testing.T) {
	// SHA256 field present but with no entries — every line is empty / has
	// fewer than 3 fields. Should error because we couldn't extract any path.
	payload := []byte(`Origin: Debian
SHA256:
`)
	_, err := ParseReleaseHashes(payload)
	if err == nil {
		t.Fatal("expected error when SHA256 field has no entries")
	}
	if !strings.Contains(err.Error(), "no SHA256 hashes") {
		t.Errorf("error should mention missing hashes: %v", err)
	}
}

func TestParseReleaseHashesSkipsMalformedLines(t *testing.T) {
	// Lines with fewer than 3 whitespace-separated fields should be silently
	// skipped, not crash the parser. A real Release file may have garbage at
	// the start or end of a multi-line field (rare, but tolerated).
	payload := []byte(`Origin: Debian
SHA256:
 onlyonefield
 two fields
 abc123def456abc123def456abc123def456abc123def456abc123def456abcd 12345 main/source/Sources.gz
 short
`)
	hashes, err := ParseReleaseHashes(payload)
	if err != nil {
		t.Fatalf("expected to parse despite malformed lines: %v", err)
	}
	if len(hashes) != 1 {
		t.Fatalf("expected 1 hash extracted, got %d", len(hashes))
	}
	if _, ok := hashes["main/source/Sources.gz"]; !ok {
		t.Errorf("expected Sources.gz path: %v", hashes)
	}
}

func TestParseReleaseHashesCaseInsensitiveField(t *testing.T) {
	// deb822 field names are case-insensitive (the parser lowercases keys).
	// Both "SHA256:" and "sha256:" should resolve to the same field.
	payload := []byte(`Origin: Debian
sha256:
 abc123def456abc123def456abc123def456abc123def456abc123def456abcd 12345 main/source/Sources.gz
`)
	hashes, err := ParseReleaseHashes(payload)
	if err != nil {
		t.Fatalf("ParseReleaseHashes failed: %v", err)
	}
	if len(hashes) != 1 {
		t.Fatalf("expected 1 hash, got %d", len(hashes))
	}
}

func TestParseReleaseHashesDuplicatePathLastWins(t *testing.T) {
	// If a path appears more than once, the last entry wins. This isn't a
	// guarantee anybody depends on, but documenting current behavior keeps
	// the test honest.
	payload := []byte(`Origin: Debian
SHA256:
 1111111111111111111111111111111111111111111111111111111111111111 100 main/x.gz
 2222222222222222222222222222222222222222222222222222222222222222 200 main/x.gz
`)
	hashes, err := ParseReleaseHashes(payload)
	if err != nil {
		t.Fatal(err)
	}
	if hashes["main/x.gz"] != "2222222222222222222222222222222222222222222222222222222222222222" {
		t.Errorf("expected last entry to win, got %s", hashes["main/x.gz"])
	}
}

// TestFetchInReleaseHTTP exercises the full HTTP path of FetchInRelease
// against an httptest server, including a forged cleartext-signed envelope.
// Signature verification is disabled (NoVerify) since real GPG keys are
// covered separately by stream/gpg_test.go.
func TestFetchInReleaseHTTP(t *testing.T) {
	releaseStanza := "Origin: Test\nSuite: testing\nSHA256:\n abc123 100 main/x\n"
	wrapped := "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\n" +
		releaseStanza +
		"-----BEGIN PGP SIGNATURE-----\nfakesig\n-----END PGP SIGNATURE-----\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dists/testing/InRelease" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(wrapped))
	}))
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	payload, err := FetchInRelease(context.Background(), FetchConfig{
		Store:    store,
		RepoURL:  srv.URL,
		Dist:     "testing",
		NoVerify: true,
	})
	if err != nil {
		t.Fatalf("FetchInRelease: %v", err)
	}

	got := string(payload)
	if !strings.Contains(got, "Origin: Test") {
		t.Errorf("payload should contain stanza fields, got: %q", got)
	}
	if strings.Contains(got, "BEGIN PGP") {
		t.Errorf("payload should have envelope stripped, got: %q", got)
	}
}

func TestFetchInReleaseHTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	_, err = FetchInRelease(context.Background(), FetchConfig{
		Store:    store,
		RepoURL:  srv.URL,
		Dist:     "testing",
		NoVerify: true,
	})
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

// TestFetchInReleaseUsesCookieCache validates that a second fetch with the
// same cookie reads from the objstore instead of hitting HTTP. The httptest
// server fails the test on a second request.
func TestFetchInReleaseUsesCookieCache(t *testing.T) {
	releaseStanza := "Origin: Test\nSuite: testing\nSHA256:\n abc123 100 main/x\n"
	wrapped := "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\n" +
		releaseStanza +
		"-----BEGIN PGP SIGNATURE-----\nfakesig\n-----END PGP SIGNATURE-----\n"

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits > 1 {
			t.Errorf("server received %d hits, expected only 1 (cache should serve subsequent calls)", hits)
		}
		w.Write([]byte(wrapped))
	}))
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	cfg := FetchConfig{
		Store:    store,
		RepoURL:  srv.URL,
		Dist:     "testing",
		Cookie:   "test-cookie",
		NoVerify: true,
	}

	first, err := FetchInRelease(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first FetchInRelease: %v", err)
	}
	second, err := FetchInRelease(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second FetchInRelease: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("cached payload differs from original")
	}
}

func TestFetchInReleaseRejectsMissingFields(t *testing.T) {
	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	cases := []FetchConfig{
		{RepoURL: "http://x", Dist: "testing"}, // no Store
		{Store: store, Dist: "testing"},        // no RepoURL
		{Store: store, RepoURL: "http://x"},    // no Dist
	}
	for i, cfg := range cases {
		if _, err := FetchInRelease(context.Background(), cfg); err == nil {
			t.Errorf("case %d: expected error for missing required field", i)
		}
	}
}

func TestSnapshotURL(t *testing.T) {
	cases := []struct {
		name       string
		base       string
		sha1, file string
		want       string
	}{
		{
			name: "default base",
			sha1: "f322085c1e2f95e8febe24989f776cfac268ff90",
			file: "hello_2.10-3_amd64.deb",
			want: "https://snapshot.debian.org/file/f322085c1e2f95e8febe24989f776cfac268ff90/hello_2.10-3_amd64.deb",
		},
		{
			name: "custom base with trailing slash",
			base: "https://mirror.example/file/",
			sha1: "abc123",
			file: "pkg_1.0_amd64.deb",
			want: "https://mirror.example/file/abc123/pkg_1.0_amd64.deb",
		},
		{
			name: "filename with special characters is escaped",
			sha1: "deadbeef",
			file: "lib foo+bar_1:2.3.deb",
			want: "https://snapshot.debian.org/file/deadbeef/lib%20foo+bar_1:2.3.deb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SnapshotURL(tc.base, tc.sha1, tc.file); got != tc.want {
				t.Errorf("SnapshotURL = %q, want %q", got, tc.want)
			}
		})
	}
}
