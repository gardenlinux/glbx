package objstore

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeOCIRegistry is a minimal in-memory OCI registry over httptest, enough to
// exercise the client's blob existence, blob pull/push, and manifest get/put by
// tag. It emulates the distribution-spec routes the store uses and nothing more.
type fakeOCIRegistry struct {
	mu        sync.Mutex
	blobs     map[string][]byte // keyed by "sha256:<hex>"
	manifests map[string][]byte // keyed by tag
	uploads   map[string][]byte // keyed by upload session id
	nextID    int
}

func newFakeOCIRegistry() *fakeOCIRegistry {
	return &fakeOCIRegistry{
		blobs:     map[string][]byte{},
		manifests: map[string][]byte{},
		uploads:   map[string][]byte{},
	}
}

func (f *fakeOCIRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	// /v2/<repo>/blobs/... or /v2/<repo>/manifests/<tag>
	if len(parts) < 4 || parts[0] != "v2" {
		http.NotFound(w, r)
		return
	}
	kind := parts[2]
	rest := parts[3:]

	switch {
	case kind == "blobs" && rest[0] == "uploads":
		// POST /v2/<repo>/blobs/uploads/ opens a session.
		f.nextID++
		id := "sess" + itoa(f.nextID)
		f.uploads[id] = nil
		w.Header().Set("Location", "/upload/"+id)
		w.WriteHeader(http.StatusAccepted)

	case kind == "blobs":
		digest := rest[0]
		data, ok := f.blobs[digest]
		switch r.Method {
		case http.MethodHead:
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(data)
		}

	case kind == "manifests":
		tag := rest[0]
		switch r.Method {
		case http.MethodHead:
			if _, ok := f.manifests[tag]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			body, ok := f.manifests[tag]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", ociManifestType)
			w.Write(body)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			f.manifests[tag] = body
			w.WriteHeader(http.StatusCreated)
		}

	default:
		http.NotFound(w, r)
	}
}

// uploadHandler completes a monolithic blob upload: PUT /upload/<id>?digest=...
func (f *fakeOCIRegistry) uploadHandler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, _ := io.ReadAll(r.Body)
	digest := r.URL.Query().Get("digest")
	if digest == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.blobs[digest] = data
	w.WriteHeader(http.StatusCreated)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// newTestRegistry starts a fake registry and returns a client pointed at it.
func newTestRegistry(t *testing.T) (*ociRegistry, *fakeOCIRegistry) {
	t.Helper()
	fake := newFakeOCIRegistry()
	mux := http.NewServeMux()
	mux.Handle("/v2/", fake)
	mux.HandleFunc("/upload/", fake.uploadHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ref := strings.TrimPrefix(srv.URL, "http://") + "/glbx-test"
	reg, err := parseRegistry("http://"+ref, false)
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	return reg, fake
}

func TestOCI_PublishAndResolveBlob(t *testing.T) {
	reg, _ := newTestRegistry(t)
	content := []byte("a stored blob")
	h := HashBytes(content)

	if err := reg.publishBlob(h, content); err != nil {
		t.Fatalf("publishBlob: %v", err)
	}

	rb := &remoteBlobs{reg: reg}
	if !rb.Has(h) {
		t.Error("published blob should exist")
	}
	rc, err := rb.Open(h)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != string(content) {
		t.Errorf("blob content %q, want %q", got, content)
	}
}

func TestOCI_AbsentBlobIsMiss(t *testing.T) {
	reg, _ := newTestRegistry(t)
	rb := &remoteBlobs{reg: reg}
	absent := MustHash("abababababababababababababababababababababababababababababababab")
	if rb.Has(absent) {
		t.Error("absent blob should not exist")
	}
	if _, err := rb.Open(absent); err == nil {
		t.Error("Open on absent blob should error")
	}
}

func TestOCI_MapResolvesToManifestHash(t *testing.T) {
	reg, _ := newTestRegistry(t)

	// Publish a manifest blob, then alias an identity to it.
	manifestBlob := []byte("deadbeef output.tar\n")
	mh := HashBytes(manifestBlob)
	if err := reg.publishBlob(mh, manifestBlob); err != nil {
		t.Fatalf("publish manifest blob: %v", err)
	}
	id := MustHash("1212121212121212121212121212121212121212121212121212121212121212")
	if err := reg.aliasMap(id, mh, int64(len(manifestBlob))); err != nil {
		t.Fatalf("aliasMap: %v", err)
	}

	rm := &remoteMap{reg: reg}
	if !rm.Has(id) {
		t.Error("aliased identity should exist")
	}
	got, err := rm.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Equal(mh) {
		t.Errorf("map resolves to %s, want manifest hash %s", got, mh)
	}
	// The map tag and the blob tag name the byte-identical manifest.
	mapManifest, _ := reg.getManifest(mapTag(id))
	blobManifest, _ := reg.getManifest(blobTag(mh))
	mapJSON, _ := json.Marshal(mapManifest)
	blobJSON, _ := json.Marshal(blobManifest)
	if string(mapJSON) != string(blobJSON) {
		t.Error("map-<id> and blob-<manifestHash> should name identical manifests")
	}
}

func TestOCI_PublishIsIdempotent(t *testing.T) {
	reg, _ := newTestRegistry(t)
	local := newLocalForTest(t)
	content := []byte("published once")
	h, _ := local.Store(strings.NewReader(string(content)))

	pub, err := NewPublisher("http://"+regRef(reg), false, local)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	up, missing, err := pub.EnsureBlob(h)
	if err != nil || missing || !up {
		t.Fatalf("first EnsureBlob: up=%v missing=%v err=%v", up, missing, err)
	}
	up, _, err = pub.EnsureBlob(h)
	if err != nil || up {
		t.Fatalf("second EnsureBlob should be a no-op: up=%v err=%v", up, err)
	}
}

func TestOCI_PublishSkipsBlobAbsentLocally(t *testing.T) {
	reg, _ := newTestRegistry(t)
	local := newLocalForTest(t)
	pub, _ := NewPublisher("http://"+regRef(reg), false, local)

	absent := MustHash("cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd")
	up, missing, err := pub.EnsureBlob(absent)
	if err != nil {
		t.Fatalf("EnsureBlob: %v", err)
	}
	if up || !missing {
		t.Errorf("a blob absent locally should be reported missing, not uploaded: up=%v missing=%v", up, missing)
	}
}

// regRef returns the host/repo of a client's base URL, for building a Publisher
// against the same fake registry.
func regRef(reg *ociRegistry) string {
	return reg.base.Host + "/" + strings.TrimPrefix(reg.base.Path, "/v2/")
}

// newLocalForTest opens a pure-local blob store in a temp dir.
func newLocalForTest(t *testing.T) BlobStore {
	t.Helper()
	s, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("Open local: %v", err)
	}
	return s.Blobs
}

// TestOCI_BearerAuth exercises the 401-challenge → token → retry flow. The fake
// registry rejects unauthenticated requests with a Bearer challenge pointing at
// a token endpoint, which issues a token for the right basic-auth credentials.
// The client must redeem and attach it transparently.
func TestOCI_BearerAuth(t *testing.T) {
	fake := newFakeOCIRegistry()

	const user, pass, token = "x-access-token", "s3cret-pat", "issued-bearer-token"

	mux := http.NewServeMux()
	// Token endpoint: basic-auth gate, returns a bearer token.
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"` + token + `"}`))
	})

	var tokenRedemptions int
	var muCount sync.Mutex
	// Registry routes: require Bearer token, else challenge.
	authGate := func(next http.Handler) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				realm := "http://" + r.Host + "/token"
				w.Header().Set("WWW-Authenticate",
					`Bearer realm="`+realm+`",service="fake",scope="repository:glbx-test:pull,push"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		}
	}
	mux.Handle("/v2/", authGate(fake))
	mux.HandleFunc("/upload/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fake.uploadHandler(w, r)
	})
	// Count redemptions by wrapping the token mux.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			muCount.Lock()
			tokenRedemptions++
			muCount.Unlock()
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	ref := strings.TrimPrefix(srv.URL, "http://") + "/glbx-test"
	reg, err := parseRegistry("http://"+ref, false)
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	reg.user, reg.pass = user, pass

	// A full publish+resolve round-trip must succeed through the auth gate.
	content := []byte("authenticated blob")
	h := HashBytes(content)
	if err := reg.publishBlob(h, content); err != nil {
		t.Fatalf("publishBlob through auth: %v", err)
	}
	rb := &remoteBlobs{reg: reg}
	if !rb.Has(h) {
		t.Error("published blob should exist")
	}
	rc, err := rb.Open(h)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != string(content) {
		t.Errorf("blob content %q, want %q", got, content)
	}

	// The token is cached: many requests, but far fewer redemptions than requests
	// (ideally one). Assert it did not redeem on every single request.
	muCount.Lock()
	n := tokenRedemptions
	muCount.Unlock()
	if n == 0 {
		t.Error("expected at least one token redemption")
	}
	if n > 3 {
		t.Errorf("token not cached: %d redemptions for a handful of requests", n)
	}
}

// TestOCI_BearerAuthWrongCreds confirms bad credentials surface as an error
// rather than silently proceeding.
func TestOCI_BearerAuthWrongCreds(t *testing.T) {
	fake := newFakeOCIRegistry()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.Handle("/v2/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate",
			`Bearer realm="http://`+r.Host+`/token",service="fake"`)
		w.WriteHeader(http.StatusUnauthorized)
		_ = fake
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ref := strings.TrimPrefix(srv.URL, "http://") + "/glbx-test"
	reg, _ := parseRegistry("http://"+ref, false)
	reg.user, reg.pass = "bad", "creds"

	if err := reg.publishBlob(HashBytes([]byte("x")), []byte("x")); err == nil {
		t.Error("expected publish to fail with bad credentials")
	}
}
