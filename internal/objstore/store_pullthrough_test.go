package objstore

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// fakeRemoteBlobs is an in-memory BlobStore for hermetic pull-through tests. It
// serves blobs by hash and refuses the write, path, and sweep operations a bare
// remote has no meaning for.
type fakeRemoteBlobs struct {
	blobs     map[string][]byte
	pullCount *int
}

func (f *fakeRemoteBlobs) Has(h Hash) bool {
	_, ok := f.blobs[h.String()]
	return ok
}

func (f *fakeRemoteBlobs) Open(h Hash) (io.ReadCloser, error) {
	b, ok := f.blobs[h.String()]
	if !ok {
		return nil, os.ErrNotExist
	}
	*f.pullCount++
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeRemoteBlobs) Path(Hash) (string, error)            { return "", errRemoteReadOnly }
func (f *fakeRemoteBlobs) Store(io.Reader) (Hash, error)        { return Hash{}, errRemoteReadOnly }
func (f *fakeRemoteBlobs) Delete(Hash) error                    { return errRemoteReadOnly }
func (f *fakeRemoteBlobs) Sweep(map[Hash]struct{}) (int, error) { return 0, errRemoteReadOnly }
func (f *fakeRemoteBlobs) Iterate(func(Hash) error) error       { return errRemoteReadOnly }

// fakeRemoteMap is an in-memory MapStore resolving an identity to a manifest-
// blob hash, as the real remote does — it parses no manifest bytes.
type fakeRemoteMap struct {
	entries  map[string]Hash
	errOnGet Hash // if set, Get for this identity returns a transport error
}

func (f *fakeRemoteMap) Has(key Hash) bool {
	_, ok := f.entries[key.String()]
	return ok
}

func (f *fakeRemoteMap) Get(key Hash) (Hash, error) {
	if !f.errOnGet.IsZero() && f.errOnGet.Equal(key) {
		return Hash{}, os.ErrInvalid
	}
	h, ok := f.entries[key.String()]
	if !ok {
		return Hash{}, os.ErrNotExist
	}
	return h, nil
}

func (f *fakeRemoteMap) Set(Hash, Hash, bool) error        { return errRemoteReadOnly }
func (f *fakeRemoteMap) Delete(Hash) error                 { return errRemoteReadOnly }
func (f *fakeRemoteMap) Iterate(func(Hash) error) error    { return errRemoteReadOnly }
func (f *fakeRemoteMap) SweepFollowingBlobs() (int, error) { return 0, errRemoteReadOnly }

// fakeRemote bundles a blob and map backend for a test store, sharing one pull
// counter so a test can assert the remote is not re-consulted after a hit.
type fakeRemote struct {
	blobs     *fakeRemoteBlobs
	manifests *fakeRemoteMap
	pullCount int
}

func newFakeRemote() *fakeRemote {
	fr := &fakeRemote{
		blobs:     &fakeRemoteBlobs{blobs: map[string][]byte{}},
		manifests: &fakeRemoteMap{entries: map[string]Hash{}},
	}
	fr.blobs.pullCount = &fr.pullCount
	return fr
}

// put stores a blob in the fake remote and returns its hash.
func (f *fakeRemote) put(b []byte) Hash {
	h := HashBytes(b)
	f.blobs.blobs[h.String()] = b
	return h
}

// publish records an artifact: it stores each output blob and the manifest blob
// that lists them, and maps the identity to the manifest-blob hash — exactly
// what a real publish leaves on the registry.
func (f *fakeRemote) publish(identity Hash, outputs []Output) Hash {
	for _, o := range outputs {
		if _, ok := f.blobs.blobs[o.Hash.String()]; !ok {
			f.blobs.blobs[o.Hash.String()] = []byte("output:" + o.Name)
		}
	}
	manifestHash := f.put([]byte(SerializeManifest(outputs)))
	f.manifests.entries[identity.String()] = manifestHash
	return manifestHash
}

// storeWithRemote composes a local store with a fake remote into a pull-through,
// returning the pull-through store and the underlying local store so a test can
// assert against the local layer directly.
func storeWithRemote(t *testing.T, r *fakeRemote) (pt, local *Store) {
	t.Helper()
	local, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	far := &Store{Blobs: r.blobs, Map: r.manifests}
	return NewPullThrough(local, far), local
}

func TestStore_NilRemote_IsPureLocal(t *testing.T) {
	s, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	absent := MustHash("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")

	if s.Blobs.Has(absent) {
		t.Error("absent blob should not be present")
	}
	if _, err := s.Blobs.Open(absent); err == nil {
		t.Error("Open(absent) with no remote should error")
	}
	if _, err := s.Map.Get(absent); err == nil {
		t.Error("Map.Get(absent) with no remote should error")
	}
}

func TestStore_EmptyRemote_BehavesLikeLocal(t *testing.T) {
	s, _ := storeWithRemote(t, newFakeRemote())
	absent := MustHash("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if s.Blobs.Has(absent) {
		t.Error("empty remote should supply nothing")
	}
	if _, err := s.Map.Get(absent); err == nil {
		t.Error("Map.Get with empty remote should report not-found")
	}
}

func TestStore_Blobs_PullsByDigest(t *testing.T) {
	fr := newFakeRemote()
	content := []byte("pulled-blob-content")
	h := fr.put(content)
	s, local := storeWithRemote(t, fr)

	rc, err := s.Blobs.Open(h)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, content) {
		t.Errorf("content mismatch: %q", got)
	}
	// The pull materialized the blob into the local layer.
	if !local.Blobs.Has(h) {
		t.Error("Open should have materialized the blob locally")
	}
}

func TestStore_Blobs_DigestMismatchIsMiss(t *testing.T) {
	fr := newFakeRemote()
	claimed := MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	fr.blobs.blobs[claimed.String()] = []byte("not what the hash says")
	s, local := storeWithRemote(t, fr)

	// Open enforces integrity: it hashes the pulled bytes and rejects them when
	// they do not match the requested digest, so a mismatched remote blob never
	// becomes a successful read and is never materialized under the claimed hash.
	if _, err := s.Blobs.Open(claimed); err == nil {
		t.Error("Open on digest-mismatched remote blob should not succeed")
	}
	if local.Blobs.Has(claimed) {
		t.Error("digest-mismatched blob must not be materialized under the claimed hash")
	}
}

func TestStore_Map_ResolvesAndMaterializesValueOnly(t *testing.T) {
	fr := newFakeRemote()

	leaf1 := fr.put([]byte("rootfs-tarball-bytes"))
	leaf2 := fr.put([]byte("control-bytes"))
	leaves := []Output{
		{Name: "rootfs.tar.gz", Hash: leaf1},
		{Name: "control:libc6", Hash: leaf2},
	}
	identity := MustHash("2222222222222222222222222222222222222222222222222222222222222222")
	wantManifest := fr.publish(identity, leaves)
	s, local := storeWithRemote(t, fr)

	manifestHash, err := s.Map.Get(identity)
	if err != nil {
		t.Fatalf("Map.Get: %v", err)
	}
	if !manifestHash.Equal(wantManifest) {
		t.Errorf("manifest hash %s, want %s", manifestHash, wantManifest)
	}
	// Resolving the map entry materializes only the one blob it points at — the
	// map treats that hash opaquely and never reads it. The outputs that blob
	// happens to name are not touched: resolvable through the pull-through (Has
	// sees the remote) but not copied locally until actually opened.
	if !local.Blobs.Has(manifestHash) {
		t.Error("map resolution should materialize the pointed-at blob locally")
	}
	for _, l := range leaves {
		if !s.Blobs.Has(l.Hash) {
			t.Errorf("leaf %s should be resolvable through the remote", l.Hash)
		}
		if local.Blobs.Has(l.Hash) {
			t.Errorf("leaf %s should not be materialized locally before it is opened", l.Hash)
		}
	}
	// Opening a leaf materializes it.
	rc0, err := s.Blobs.Open(leaf1)
	if err != nil {
		t.Fatalf("open leaf: %v", err)
	}
	rc0.Close()
	if !local.Blobs.Has(leaf1) {
		t.Error("opening a leaf should materialize it locally")
	}
	rc, err := s.Blobs.Open(manifestHash)
	if err != nil {
		t.Fatalf("open pulled manifest: %v", err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != SerializeManifest(leaves) {
		t.Errorf("pulled manifest not byte-identical:\n got: %q\nwant: %q", body, SerializeManifest(leaves))
	}
	// Second resolution is a local hit (no further remote lookups).
	before := fr.pullCount
	if _, err := s.Map.Get(identity); err != nil {
		t.Errorf("second Map.Get should be a local hit: %v", err)
	}
	if fr.pullCount != before {
		t.Error("second Map.Get should not touch the remote")
	}
}

func TestStore_Map_RemoteMissIsNotFound(t *testing.T) {
	s, _ := storeWithRemote(t, newFakeRemote())
	absent := MustHash("3333333333333333333333333333333333333333333333333333333333333333")
	if _, err := s.Map.Get(absent); err == nil {
		t.Error("Map.Get on a remote miss should still report not-found")
	}
}

func TestStore_Map_RemoteErrorPropagates(t *testing.T) {
	fr := newFakeRemote()
	id := MustHash("4444444444444444444444444444444444444444444444444444444444444444")
	fr.manifests.errOnGet = id
	s, _ := storeWithRemote(t, fr)
	if _, err := s.Map.Get(id); err == nil {
		t.Error("Map.Get should propagate a remote error")
	}
}
