package objstore

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// fakeRemote is an in-memory Remote for hermetic pull-through tests.
type fakeRemote struct {
	blobs     map[string][]byte
	manifests map[string][]Output
	pullCount int
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{blobs: map[string][]byte{}, manifests: map[string][]Output{}}
}

func (f *fakeRemote) OpenBlob(h Hash) (io.ReadCloser, error) {
	b, ok := f.blobs[h.String()]
	if !ok {
		return nil, os.ErrNotExist
	}
	f.pullCount++
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeRemote) ManifestLeaves(identity Hash) ([]Output, bool, error) {
	leaves, ok := f.manifests[identity.String()]
	if !ok {
		return nil, false, nil
	}
	return leaves, true, nil
}

func storeWithRemote(t *testing.T, r Remote) *Store {
	t.Helper()
	s, err := OpenWithRemote(t.TempDir(), r)
	if err != nil {
		t.Fatalf("OpenWithRemote: %v", err)
	}
	return s
}

func TestStore_NilRemote_IsPureLocal(t *testing.T) {
	s, err := Open(t.TempDir())
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

func TestStore_NopRemote_BehavesLikeLocal(t *testing.T) {
	s := storeWithRemote(t, nopRemote{})
	absent := MustHash("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if s.Blobs.Has(absent) {
		t.Error("nop remote should supply nothing")
	}
	if _, err := s.Map.Get(absent); err == nil {
		t.Error("Map.Get with nop remote should report not-found")
	}
}

func TestStore_Blobs_PullsByDigest(t *testing.T) {
	fr := newFakeRemote()
	content := []byte("pulled-blob-content")
	h := HashBytes(content)
	fr.blobs[h.String()] = content
	s := storeWithRemote(t, fr)

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
	local, err := Open(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	if !local.Blobs.Has(h) {
		t.Error("Open should have materialized the blob locally")
	}
}

func TestStore_Blobs_DigestMismatchIsMiss(t *testing.T) {
	fr := newFakeRemote()
	claimed := MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	fr.blobs[claimed.String()] = []byte("not what the hash says")
	s := storeWithRemote(t, fr)

	if _, err := s.Blobs.Open(claimed); err == nil {
		t.Error("Open on digest-mismatched remote blob should not succeed")
	}
	if s.Blobs.Has(claimed) {
		t.Error("digest-mismatched blob must not be present under the claimed hash")
	}
}

func TestStore_Map_ReconstructsManifestByteIdentical(t *testing.T) {
	fr := newFakeRemote()

	leaf1 := []byte("rootfs-tarball-bytes")
	leaf2 := []byte("control-bytes")
	h1 := HashBytes(leaf1)
	h2 := HashBytes(leaf2)
	fr.blobs[h1.String()] = leaf1
	fr.blobs[h2.String()] = leaf2

	identity := MustHash("2222222222222222222222222222222222222222222222222222222222222222")
	leaves := []Output{
		{Name: "rootfs.tar.gz", Hash: h1},
		{Name: "control:libc6", Hash: h2},
	}
	fr.manifests[identity.String()] = leaves
	s := storeWithRemote(t, fr)

	manifestHash, err := s.Map.Get(identity)
	if err != nil {
		t.Fatalf("Map.Get: %v", err)
	}
	for _, l := range leaves {
		if !s.Blobs.Has(l.Hash) {
			t.Errorf("leaf %s not pulled", l.Hash)
		}
	}
	rc, err := s.Blobs.Open(manifestHash)
	if err != nil {
		t.Fatalf("open reconstructed manifest: %v", err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != SerializeManifest(leaves) {
		t.Errorf("reconstructed manifest not byte-identical:\n got: %q\nwant: %q", body, SerializeManifest(leaves))
	}
	// Second resolution is a local hit (no further remote manifest lookups).
	before := fr.pullCount
	if _, err := s.Map.Get(identity); err != nil {
		t.Errorf("second Map.Get should be a local hit: %v", err)
	}
	if fr.pullCount != before {
		t.Error("second Map.Get should not touch the remote")
	}
}

func TestStore_Map_RemoteMissIsNotFound(t *testing.T) {
	s := storeWithRemote(t, newFakeRemote())
	absent := MustHash("3333333333333333333333333333333333333333333333333333333333333333")
	if _, err := s.Map.Get(absent); err == nil {
		t.Error("Map.Get on a remote miss should still report not-found")
	}
}

func TestStore_Map_RemoteErrorPropagates(t *testing.T) {
	s := storeWithRemote(t, errRemote{})
	id := MustHash("4444444444444444444444444444444444444444444444444444444444444444")
	if _, err := s.Map.Get(id); err == nil {
		t.Error("Map.Get should propagate a remote error")
	}
}

type errRemote struct{}

func (errRemote) OpenBlob(Hash) (io.ReadCloser, error) {
	return nil, os.ErrInvalid
}

func (errRemote) ManifestLeaves(Hash) ([]Output, bool, error) {
	return nil, false, os.ErrInvalid
}
