package objstore

import (
	"bytes"
	"errors"
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

func (f *fakeRemote) PullBlobByDigest(h Hash) (io.ReadCloser, int64, error) {
	b, ok := f.blobs[h.String()]
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	f.pullCount++
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

func (f *fakeRemote) PullOutputManifest(identity Hash) ([]Output, bool, error) {
	leaves, ok := f.manifests[identity.String()]
	if !ok {
		return nil, false, nil
	}
	return leaves, true, nil
}

func storeAt(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestStore_NilRemote_NoFallthrough(t *testing.T) {
	s := storeAt(t)
	absent := MustHash("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")

	if _, err := s.OpenBlob(absent); err == nil {
		t.Error("OpenBlob(absent) with nil remote should error")
	}
	if _, err := s.MapGet(absent); err == nil {
		t.Error("MapGet(absent) with nil remote should error")
	}
	if err := s.EnsureBlob(absent); err != nil {
		t.Errorf("EnsureBlob with nil remote should be nil, got %v", err)
	}
	if s.HasRemote() {
		t.Error("HasRemote should be false")
	}
}

func TestStore_OpenBlob_PullsByDigest(t *testing.T) {
	s := storeAt(t)
	fr := newFakeRemote()
	content := []byte("pulled-blob-content")
	h := HashBytes(content)
	fr.blobs[h.String()] = content
	s.SetRemote(fr)

	if s.Blobs.Has(h) {
		t.Fatal("blob should not be local yet")
	}
	rc, err := s.OpenBlob(h)
	if err != nil {
		t.Fatalf("OpenBlob: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, content) {
		t.Errorf("content mismatch: %q", got)
	}
	if !s.Blobs.Has(h) {
		t.Error("OpenBlob should have materialized the blob locally")
	}
}

func TestStore_OpenBlob_DigestMismatchIsMiss(t *testing.T) {
	s := storeAt(t)
	fr := newFakeRemote()
	claimed := MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	fr.blobs[claimed.String()] = []byte("not what the hash says")
	s.SetRemote(fr)

	if _, err := s.OpenBlob(claimed); err == nil {
		t.Error("OpenBlob on digest-mismatched remote blob should not succeed")
	}
	if s.Blobs.Has(claimed) {
		t.Error("digest-mismatched blob must not be stored under the claimed hash")
	}
}

func TestStore_MapGet_ReconstructsManifestByteIdentical(t *testing.T) {
	s := storeAt(t)
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
	s.SetRemote(fr)

	manifestHash, err := s.MapGet(identity)
	if err != nil {
		t.Fatalf("MapGet: %v", err)
	}
	for _, l := range leaves {
		if !s.Blobs.Has(l.Hash) {
			t.Errorf("leaf %s not pulled", l.Hash)
		}
	}
	if got, err := s.Map.Get(identity); err != nil || !got.Equal(manifestHash) {
		t.Errorf("map entry not set: got %v err %v", got, err)
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
	if _, err := s.MapGet(identity); err != nil {
		t.Errorf("second MapGet should be a local hit: %v", err)
	}
}

func TestStore_MapGet_RemoteMissIsNotFound(t *testing.T) {
	s := storeAt(t)
	s.SetRemote(newFakeRemote())
	absent := MustHash("3333333333333333333333333333333333333333333333333333333333333333")
	if _, err := s.MapGet(absent); err == nil {
		t.Error("MapGet on a remote miss should still report not-found")
	}
}

func TestStore_MapGet_RemoteErrorPropagates(t *testing.T) {
	s := storeAt(t)
	s.SetRemote(errRemote{})
	id := MustHash("4444444444444444444444444444444444444444444444444444444444444444")
	if _, err := s.MapGet(id); err == nil {
		t.Error("MapGet should propagate a remote error")
	}
}

type errRemote struct{}

func (errRemote) PullBlobByDigest(Hash) (io.ReadCloser, int64, error) {
	return nil, 0, errors.New("boom")
}

func (errRemote) PullOutputManifest(Hash) ([]Output, bool, error) {
	return nil, false, errors.New("boom")
}
