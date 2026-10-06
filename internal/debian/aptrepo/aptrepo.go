// Package aptrepo handles APT-repository metadata fetching that is shared
// between the importer (which walks Sources indexes) and the lockfile
// generator (which walks Packages indexes).
//
// Both consumers need to fetch and trust an InRelease file, then look up
// content-addressed hashes for the index files they actually care about. The
// FetchInRelease primitive owns:
//
//   - the optional cookie-keyed blob cache that lets repeated invocations skip
//     the round-trip to the mirror,
//   - the GPG signature verification + cleartext stripping,
//   - the http.Get itself.
//
// ParseReleaseHashes extracts the SHA256 path → hash map from the Release
// payload that FetchInRelease returns.
package aptrepo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gardenlinux/glbx/internal/debian/deb822"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/stream"
)

// FetchConfig describes how to retrieve an InRelease file. Store, RepoURL and
// Dist are required; the rest are optional.
type FetchConfig struct {
	Store   *objstore.Store
	RepoURL string
	Dist    string

	// Cookie, if non-empty, keys an objstore.Map entry that caches the
	// downloaded InRelease bytes across invocations. Callers compose cookies
	// from the trust context they care about (e.g., the release date).
	Cookie string

	// Keyring is the path to a GPG keyring used to verify the InRelease
	// signature. Ignored when NoVerify is true.
	Keyring string

	// NoVerify skips signature verification and just strips the cleartext
	// envelope. Use only when callers have an out-of-band trust path.
	NoVerify bool

	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client

	// Component categorises log messages emitted during the fetch. Defaults
	// to log.Fetch when zero.
	Component log.Component
}

// FetchInRelease retrieves the InRelease file for the configured dist and
// returns the verified-and-stripped Release payload (the deb822 stanza body).
// On a cookie-cache hit the payload is read from the object store; otherwise
// it is fetched over HTTP and (when Cookie is set) cached for next time.
func FetchInRelease(ctx context.Context, cfg FetchConfig) ([]byte, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("aptrepo: Store is required")
	}
	if cfg.RepoURL == "" {
		return nil, fmt.Errorf("aptrepo: RepoURL is required")
	}
	if cfg.Dist == "" {
		return nil, fmt.Errorf("aptrepo: Dist is required")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	component := cfg.Component
	if component == 0 {
		component = log.Fetch
	}
	l := log.From(ctx, component)

	inReleaseData, err := loadOrDownloadInRelease(cfg, client, l)
	if err != nil {
		return nil, err
	}

	if !cfg.NoVerify && cfg.Keyring != "" {
		payload, err := stream.GPGVerifyClearSigned(cfg.Keyring, bytes.NewReader(inReleaseData))
		if err != nil {
			return nil, fmt.Errorf("verifying InRelease signature: %w", err)
		}
		return payload, nil
	}
	payload, err := stream.ExtractClearSignedPayload(inReleaseData)
	if err != nil {
		return nil, fmt.Errorf("extracting InRelease payload: %w", err)
	}
	return payload, nil
}

func loadOrDownloadInRelease(cfg FetchConfig, client *http.Client, l *log.Logger) ([]byte, error) {
	url := fmt.Sprintf("%s/dists/%s/InRelease", cfg.RepoURL, cfg.Dist)
	if cfg.Cookie == "" {
		l.Info("downloading %s", url)
		return httpGet(client, url)
	}

	cookieKey := inReleaseCacheKey(cfg.Cookie, cfg.RepoURL, cfg.Dist)
	if blobHash, err := cfg.Store.Map.Get(cookieKey); err == nil {
		l.Debug("InRelease from cookie cache (%s)", cookieKey.Short())
		rc, err := cfg.Store.Blobs.Open(blobHash)
		if err != nil {
			return nil, fmt.Errorf("reading cached InRelease: %w", err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return nil, fmt.Errorf("reading cached InRelease: %w", err)
		}
		return data, nil
	}

	l.Info("downloading %s", url)
	data, err := httpGet(client, url)
	if err != nil {
		return nil, err
	}
	bh, err := cfg.Store.Blobs.Store(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("caching InRelease: %w", err)
	}
	if err := cfg.Store.Map.Set(cookieKey, bh, true); err != nil {
		l.Warn("failed to set InRelease cookie map entry: %s", err)
	}
	return data, nil
}

// inReleaseCacheKey derives the object-store map key under which an InRelease
// file is cached, from the fetch coordinates and a caller-supplied cookie.
func inReleaseCacheKey(cookie, repoURL, dist string) objstore.Hash {
	return objstore.HashBytes([]byte("InRelease:" + repoURL + ":" + dist + ":" + cookie))
}

func httpGet(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ParseReleaseHashes extracts the SHA256 path-to-hex-hash map from a Release
// stanza payload (as returned by FetchInRelease).
func ParseReleaseHashes(payload []byte) (map[string]string, error) {
	reader := deb822.NewReader(bytes.NewReader(payload))
	stanza, err := reader.Next()
	if err != nil {
		return nil, fmt.Errorf("parsing Release stanza: %w", err)
	}
	sha256Field, ok := stanza["sha256"]
	if !ok {
		return nil, fmt.Errorf("Release file missing SHA256 field")
	}
	hashes := make(map[string]string)
	for _, line := range strings.Split(sha256Field, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		hashes[fields[2]] = fields[0]
	}
	if len(hashes) == 0 {
		return nil, fmt.Errorf("no SHA256 hashes found in Release file")
	}
	return hashes, nil
}

// ParseReleaseDate returns the Release file's Date field verbatim (an
// RFC1123-style string, e.g. "Sat, 01 Mar 2026 00:00:00 UTC"). Returns an
// empty string and no error when the field is absent.
func ParseReleaseDate(payload []byte) (string, error) {
	reader := deb822.NewReader(bytes.NewReader(payload))
	stanza, err := reader.Next()
	if err != nil {
		return "", fmt.Errorf("parsing Release stanza: %w", err)
	}
	return strings.TrimSpace(stanza["date"]), nil
}

// DefaultSnapshotBase is the hash-addressed file endpoint of the Debian
// snapshot archive. Files there are keyed by their SHA-1 digest, independent of
// any suite or timestamp, which makes a snapshot URL a stable second source for
// any file already pinned by content.
const DefaultSnapshotBase = "https://snapshot.debian.org/file"

// SnapshotURL builds the snapshot retrieval URL for a file given its SHA-1
// digest and name. base is the file endpoint (DefaultSnapshotBase when empty);
// name becomes the trailing path segment so the download keeps its filename.
func SnapshotURL(base, sha1, name string) string {
	if base == "" {
		base = DefaultSnapshotBase
	}
	return fmt.Sprintf("%s/%s/%s", strings.TrimRight(base, "/"), sha1, url.PathEscape(name))
}
