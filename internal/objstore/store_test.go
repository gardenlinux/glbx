package objstore

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStore_Open(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLocal(dir)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	if store.Blobs == nil {
		t.Error("Blobs should not be nil")
	}
	if store.Map == nil {
		t.Error("Map should not be nil")
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs")); err != nil {
		t.Errorf("blobs directory not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "map")); err != nil {
		t.Errorf("map directory not created: %v", err)
	}
}

func TestStore_OpenCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "store")
	if _, err := NewLocal(dir); err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs")); err != nil {
		t.Errorf("store not created under %q: %v", dir, err)
	}
}

func TestStore_OpenEmptyUsesDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GLBX_CACHE", dir)
	if _, err := NewLocal(""); err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs")); err != nil {
		t.Errorf("store not created under GLBX_CACHE %q: %v", dir, err)
	}
}

func TestStore_CacheEnvVar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GLBX_CACHE", dir)
	if root := DefaultRoot(); root != dir {
		t.Errorf("DefaultRoot() = %q, want %q", root, dir)
	}
}

func TestStore_DefaultRootWithoutEnv(t *testing.T) {
	t.Setenv("GLBX_CACHE", "")
	root := DefaultRoot()
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".cache", "glbx")
	if root != expected {
		t.Errorf("DefaultRoot() = %q, want %q", root, expected)
	}
}

func TestStore_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLocal(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	content := []byte("end-to-end test content")
	blobHash, err := store.Blobs.Store(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Blobs.Store: %v", err)
	}
	key := ConcatHash("my-artifact", "amd64")
	if err := store.Map.Set(key, blobHash, true); err != nil {
		t.Fatalf("Map.Set: %v", err)
	}
	got, err := store.Map.Get(key)
	if err != nil {
		t.Fatalf("Map.Get: %v", err)
	}
	if !got.Equal(blobHash) {
		t.Errorf("Map.Get = %s, want %s", got, blobHash)
	}
	rc, err := store.Blobs.Open(got)
	if err != nil {
		t.Fatalf("Blobs.Open: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if !bytes.Equal(data, content) {
		t.Errorf("content = %q, want %q", data, content)
	}
}

func TestStore_MapValidation(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLocal(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	key := MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	nonExistentBlob := MustHash("2222222222222222222222222222222222222222222222222222222222222222")

	err = store.Map.Set(key, nonExistentBlob, true)
	if err == nil {
		t.Error("Map.Set with validation should fail for non-existent blob")
	}
	if !strings.Contains(err.Error(), "does not reference an existing blob") {
		t.Errorf("unexpected error: %v", err)
	}
	if err := store.Map.Set(key, nonExistentBlob, false); err != nil {
		t.Fatalf("Map.Set without validation: %v", err)
	}
}

func TestStore_Reopen(t *testing.T) {
	dir := t.TempDir()
	store1, err := NewLocal(dir)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	content := []byte("persistent data")
	blobHash, _ := store1.Blobs.Store(bytes.NewReader(content))
	key := MustHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1111")
	store1.Map.Set(key, blobHash, false)

	store2, err := NewLocal(dir)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	if !store2.Blobs.Has(blobHash) {
		t.Error("blob should persist across reopen")
	}
	got, err := store2.Map.Get(key)
	if err != nil {
		t.Fatalf("Map.Get after reopen: %v", err)
	}
	if !got.Equal(blobHash) {
		t.Errorf("Map.Get = %s, want %s", got, blobHash)
	}
}

// GC keeps the manifest and output blobs an artifact names, dropping an
// unrelated blob and the map entry that pointed at a now-gone manifest.
func TestStore_GC(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLocal(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	out, _ := store.Blobs.Store(strings.NewReader("output bytes"))
	manifest, _ := store.Blobs.Store(strings.NewReader(SerializeManifest([]Output{{Name: "x", Hash: out}})))
	identity := ConcatHash("artifact", "amd64")
	if err := store.Map.Set(identity, manifest, true); err != nil {
		t.Fatalf("Map.Set: %v", err)
	}

	// An unrelated blob and its map entry — not in the keep-set.
	stale, _ := store.Blobs.Store(strings.NewReader("stale output"))
	staleID := ConcatHash("stale", "amd64")
	store.Map.Set(staleID, stale, true)

	keep := map[Hash]struct{}{manifest: {}, out: {}}
	blobs, entries, err := store.GC(keep)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if blobs != 1 {
		t.Errorf("GC deleted %d blobs, want 1", blobs)
	}
	if entries != 1 {
		t.Errorf("GC deleted %d map entries, want 1", entries)
	}
	if !store.Blobs.Has(manifest) || !store.Blobs.Has(out) {
		t.Error("kept blobs were deleted")
	}
	if !store.Map.Has(identity) {
		t.Error("map entry for kept artifact was deleted")
	}
	if store.Blobs.Has(stale) {
		t.Error("stale blob survived GC")
	}
	if store.Map.Has(staleID) {
		t.Error("stale map entry survived GC")
	}
}
