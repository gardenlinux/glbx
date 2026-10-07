package objstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// errRemoteReadOnly is returned by the write and path operations of the remote
// backend. The registry is filled by the separate publish pass, never built
// into, and has no local file to point at.
var errRemoteReadOnly = errors.New("remote store is read-only and path-less")

// OCI media types for the minimal durable unit: a one-layer image manifest
// whose config is the shared empty object and whose single layer is a stored
// blob.
const (
	ociManifestType = "application/vnd.oci.image.manifest.v1+json"
	ociEmptyType    = "application/vnd.oci.empty.v1+json"
	ociLayerType    = "application/vnd.oci.image.layer.v1.tar"
)

// ociEmptyConfig is the shared empty config descriptor every one-layer manifest
// references: the two-byte object "{}" and its digest. The registry stores it
// once and every manifest keeps it alive.
var ociEmptyConfig = descriptor{
	MediaType: ociEmptyType,
	Digest:    "sha256:" + hex.EncodeToString(sha256Sum([]byte("{}"))),
	Size:      2,
}

func sha256Sum(b []byte) []byte {
	d := sha256.Sum256(b)
	return d[:]
}

// isNotExist reports whether err signals an absent remote object (a miss) as
// opposed to a transport or protocol failure. A miss falls back to the local
// not-found; any other error is surfaced.
func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

// descriptor is an OCI content descriptor: a media type, a digest, and a size.
type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// manifest is a minimal OCI image manifest. A stored blob is wrapped in one of
// these — the shared empty config and a single layer naming the blob — so a tag
// has a manifest to root and the registry keeps the blob reachable.
type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

// oneLayerManifest builds the manifest that wraps a single blob of the given
// hash and byte size as its one layer.
func oneLayerManifest(blob Hash, size int64) manifest {
	return manifest{
		SchemaVersion: 2,
		MediaType:     ociManifestType,
		Config:        ociEmptyConfig,
		Layers: []descriptor{{
			MediaType: ociLayerType,
			Digest:    "sha256:" + blob.String(),
			Size:      size,
		}},
	}
}

// blobTag and mapTag are the two tag namespaces. A conceptual "blob/<hash>" and
// "map/<id>" (remote-cache.md) are spelled on the wire with a hyphen, because an
// OCI tag may not contain a slash; the hash/identity hex is itself a legal tag
// body.
func blobTag(h Hash) string { return "blob-" + h.String() }
func mapTag(id Hash) string { return "map-" + id.String() }

// ociRegistry is a client for one repository on an OCI registry. It speaks the
// handful of distribution-spec routes the store needs: blob existence, blob
// pull and push, and manifest get and put by tag.
type ociRegistry struct {
	base   *url.URL // scheme://host/v2/<repo>
	client *http.Client

	// user and pass are the registry credentials used to redeem a bearer token
	// against a 401 challenge. Empty means no authentication is attempted, which
	// is correct for an anonymous or local test registry.
	user string
	pass string

	mu     sync.Mutex
	bearer string // cached bearer token, redeemed lazily on the first challenge
}

// parseRegistry turns a reference "host[:port]/repo[/path]" into a client.
// The scheme is https unless the reference carries an explicit http:// prefix
// or insecure is set, which selects plaintext for a local test registry.
func parseRegistry(ref string, insecure bool) (*ociRegistry, error) {
	scheme := "https"
	switch {
	case strings.HasPrefix(ref, "http://"):
		scheme = "http"
		ref = strings.TrimPrefix(ref, "http://")
	case strings.HasPrefix(ref, "https://"):
		ref = strings.TrimPrefix(ref, "https://")
	case insecure:
		scheme = "http"
	}

	host, repo, ok := strings.Cut(ref, "/")
	if !ok || host == "" || repo == "" {
		return nil, fmt.Errorf("registry reference %q must be host/repo", ref)
	}

	base, err := url.Parse(fmt.Sprintf("%s://%s/v2/%s", scheme, host, repo))
	if err != nil {
		return nil, fmt.Errorf("registry reference %q: %w", ref, err)
	}
	return &ociRegistry{
		base:   base,
		client: &http.Client{Timeout: 10 * time.Minute},
		user:   os.Getenv("GLBX_REGISTRY_USER"),
		pass:   os.Getenv("GLBX_REGISTRY_TOKEN"),
	}, nil
}

// digestRef returns the "sha256:<hex>" reference for a hash.
func digestRef(h Hash) string { return "sha256:" + h.String() }

// urlf builds a URL under the repository base from path segments already joined.
func (r *ociRegistry) urlf(suffix string) string {
	return r.base.String() + suffix
}

// do issues an authenticated request, redeeming and caching a bearer token on a
// 401 Bearer challenge and retrying once. body, when non-nil, is the request
// payload: it is passed explicitly so the request can be rebuilt for the retry
// (an http.Request body is single-use). headers set on the first request are
// reapplied to the retry. With no credentials configured do simply issues the
// request, so an anonymous or local registry behaves exactly as before.
func (r *ociRegistry) do(method, url string, body []byte, headers map[string]string) (*http.Response, error) {
	send := func() (*http.Response, error) {
		var rdr io.Reader
		if body != nil {
			rdr = strings.NewReader(string(body))
		}
		req, err := http.NewRequest(method, url, rdr)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.ContentLength = int64(len(body))
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		r.mu.Lock()
		tok := r.bearer
		r.mu.Unlock()
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		return r.client.Do(req)
	}

	resp, err := send()
	if err != nil {
		return nil, err
	}
	// Redeem a token against a Bearer challenge and retry once. Only attempt it
	// when credentials are configured and we have not already sent a token.
	if resp.StatusCode == http.StatusUnauthorized && r.pass != "" {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		if err := r.authenticate(challenge); err != nil {
			return nil, err
		}
		return send()
	}
	return resp, nil
}

// authenticate redeems a bearer token from the realm named in a Bearer
// WWW-Authenticate challenge, using the configured credentials as HTTP basic
// auth, and caches it. The challenge looks like:
//
//	Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:owner/repo:pull,push"
func (r *ociRegistry) authenticate(challenge string) error {
	params := parseChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return fmt.Errorf("registry auth: no realm in challenge %q", challenge)
	}

	u, err := url.Parse(realm)
	if err != nil {
		return fmt.Errorf("registry auth: bad realm %q: %w", realm, err)
	}
	q := u.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(r.user, r.pass)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("registry auth: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry auth: token endpoint HTTP %d", resp.StatusCode)
	}

	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return fmt.Errorf("registry auth: decode token: %w", err)
	}
	t := tok.Token
	if t == "" {
		t = tok.AccessToken
	}
	if t == "" {
		return fmt.Errorf("registry auth: token endpoint returned no token")
	}

	r.mu.Lock()
	r.bearer = t
	r.mu.Unlock()
	return nil
}

// parseChallenge extracts the key="value" parameters from a Bearer
// WWW-Authenticate header. Keys without the Bearer scheme prefix are ignored.
func parseChallenge(challenge string) map[string]string {
	out := make(map[string]string)
	rest := strings.TrimSpace(challenge)
	if i := strings.IndexByte(rest, ' '); i >= 0 && strings.EqualFold(rest[:i], "Bearer") {
		rest = rest[i+1:]
	}
	for _, part := range strings.Split(rest, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out
}

// hasBlob reports whether a blob of the given digest is present on the registry.
func (r *ociRegistry) hasBlob(h Hash) (bool, error) {
	resp, err := r.do(http.MethodHead, r.urlf("/blobs/"+digestRef(h)), nil, nil)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("HEAD blob %s: HTTP %d", h, resp.StatusCode)
	}
}

// getBlob fetches a blob's bytes by digest.
func (r *ociRegistry) getBlob(h Hash) (io.ReadCloser, error) {
	resp, err := r.do(http.MethodGet, r.urlf("/blobs/"+digestRef(h)), nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("blob %s: %w", h, os.ErrNotExist)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET blob %s: HTTP %d", h, resp.StatusCode)
	}
	return resp.Body, nil
}

// putBlob uploads a blob monolithically: a POST opens an upload session, a PUT
// with the digest query completes it. Uploading content already present is
// harmless.
func (r *ociRegistry) putBlob(h Hash, data []byte) error {
	resp, err := r.do(http.MethodPost, r.urlf("/blobs/uploads/"), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("POST upload for %s: HTTP %d", h, resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return fmt.Errorf("POST upload for %s: no Location header", h)
	}

	putURL, err := r.resolveLocation(loc)
	if err != nil {
		return err
	}
	sep := "?"
	if strings.Contains(putURL, "?") {
		sep = "&"
	}
	putURL += sep + "digest=" + url.QueryEscape(digestRef(h))

	resp, err = r.do(http.MethodPut, putURL, data, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("PUT blob %s: HTTP %d", h, resp.StatusCode)
	}
	return nil
}

// resolveLocation turns an upload Location (which may be absolute or
// repository-relative) into an absolute URL against the registry host.
func (r *ociRegistry) resolveLocation(loc string) (string, error) {
	u, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("parsing upload location %q: %w", loc, err)
	}
	return r.base.ResolveReference(u).String(), nil
}

// getManifest fetches the one-layer manifest a tag resolves to.
func (r *ociRegistry) getManifest(tag string) (*manifest, error) {
	resp, err := r.do(http.MethodGet, r.urlf("/manifests/"+tag), nil, map[string]string{"Accept": ociManifestType})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("manifest %s: %w", tag, os.ErrNotExist)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET manifest %s: HTTP %d", tag, resp.StatusCode)
	}
	var m manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("decoding manifest %s: %w", tag, err)
	}
	return &m, nil
}

// putManifest uploads a one-layer manifest under a tag.
func (r *ociRegistry) putManifest(tag string, m manifest) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	resp, err := r.do(http.MethodPut, r.urlf("/manifests/"+tag), body, map[string]string{"Content-Type": ociManifestType})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("PUT manifest %s: HTTP %d", tag, resp.StatusCode)
	}
	return nil
}

// tagExists reports whether a tag resolves to a manifest on the registry.
func (r *ociRegistry) tagExists(tag string) (bool, error) {
	resp, err := r.do(http.MethodHead, r.urlf("/manifests/"+tag), nil, map[string]string{"Accept": ociManifestType})
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("HEAD manifest %s: HTTP %d", tag, resp.StatusCode)
	}
}

// layerHash reads the single layer digest of a one-layer manifest as a Hash.
func (m *manifest) layerHash() (Hash, error) {
	if len(m.Layers) != 1 {
		return Hash{}, fmt.Errorf("expected one-layer manifest, got %d layers", len(m.Layers))
	}
	d := m.Layers[0].Digest
	hex, ok := strings.CutPrefix(d, "sha256:")
	if !ok {
		return Hash{}, fmt.Errorf("layer digest %q is not sha256", d)
	}
	return NewHash(hex)
}

// publishBlob stores a blob durably under its blob-<hash> tag: push the shared
// empty config, the blob, and the one-layer manifest wrapping it, then tag that
// manifest. It is idempotent — re-pushing identical content is harmless.
func (r *ociRegistry) publishBlob(h Hash, data []byte) error {
	if err := r.putBlob(hashOf(ociEmptyConfig.Digest), []byte("{}")); err != nil {
		return fmt.Errorf("push empty config: %w", err)
	}
	if err := r.putBlob(h, data); err != nil {
		return fmt.Errorf("push blob %s: %w", h, err)
	}
	m := oneLayerManifest(h, int64(len(data)))
	if err := r.putManifest(blobTag(h), m); err != nil {
		return fmt.Errorf("tag blob %s: %w", h, err)
	}
	return nil
}

// aliasMap points the map-<id> tag at the one-layer manifest wrapping the
// manifest blob of the given hash and size — the byte-identical manifest
// blob-<manifestHash> already names. It introduces no object beyond the extra
// tag.
func (r *ociRegistry) aliasMap(id, manifestHash Hash, manifestSize int64) error {
	m := oneLayerManifest(manifestHash, manifestSize)
	return r.putManifest(mapTag(id), m)
}

// hashOf parses a "sha256:<hex>" descriptor digest back into a Hash. It is used
// only for the shared empty config, whose digest is a compile-time constant.
func hashOf(digest string) Hash {
	return MustHash(strings.TrimPrefix(digest, "sha256:"))
}

// remoteBlobs is the registry-backed BlobStore. A blob is read by resolving its
// blob-<hash> tag to a one-layer manifest and fetching the single layer. Writes,
// paths, and sweeps have no meaning on a bare remote and are refused; the
// pull-through store supplies them from its local member.
type remoteBlobs struct {
	reg *ociRegistry
}

func (b *remoteBlobs) Has(h Hash) bool {
	ok, err := b.reg.tagExists(blobTag(h))
	return err == nil && ok
}

// Open resolves blob-<hash> to its one-layer manifest, reads the single layer's
// digest, and streams that blob. The bytes verify against the hash that asked
// for them: an identity-mismatched layer digest or an absent tag is a miss.
func (b *remoteBlobs) Open(h Hash) (io.ReadCloser, error) {
	m, err := b.reg.getManifest(blobTag(h))
	if err != nil {
		return nil, err
	}
	layer, err := m.layerHash()
	if err != nil {
		return nil, err
	}
	if !layer.Equal(h) {
		return nil, fmt.Errorf("blob %s: manifest names a different layer %s", h, layer)
	}
	return b.reg.getBlob(layer)
}

func (b *remoteBlobs) Path(Hash) (string, error)            { return "", errRemoteReadOnly }
func (b *remoteBlobs) Store(io.Reader) (Hash, error)        { return Hash{}, errRemoteReadOnly }
func (b *remoteBlobs) Delete(Hash) error                    { return errRemoteReadOnly }
func (b *remoteBlobs) Sweep(map[Hash]struct{}) (int, error) { return 0, errRemoteReadOnly }
func (b *remoteBlobs) Iterate(func(Hash) error) error       { return errRemoteReadOnly }

// remoteMap is the registry-backed MapStore. An identity is resolved by reading
// the map-<id> tag's one-layer manifest and returning its single layer's digest,
// which is the manifest-blob's hash — the map value. The remote parses no
// manifest-blob bytes; recovering the outputs is the pull-through store's job.
type remoteMap struct {
	reg *ociRegistry
}

func (m *remoteMap) Has(key Hash) bool {
	ok, err := m.reg.tagExists(mapTag(key))
	return err == nil && ok
}

func (m *remoteMap) Get(key Hash) (Hash, error) {
	man, err := m.reg.getManifest(mapTag(key))
	if err != nil {
		return Hash{}, err
	}
	return man.layerHash()
}

func (m *remoteMap) Set(Hash, Hash, bool) error        { return errRemoteReadOnly }
func (m *remoteMap) Delete(Hash) error                 { return errRemoteReadOnly }
func (m *remoteMap) Iterate(func(Hash) error) error    { return errRemoteReadOnly }
func (m *remoteMap) SweepFollowingBlobs() (int, error) { return 0, errRemoteReadOnly }
