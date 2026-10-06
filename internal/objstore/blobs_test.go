package objstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
)

func setupBlobs(t *testing.T) *localBlobs {
	t.Helper()
	b, err := newBlobs(t.TempDir())
	if err != nil {
		t.Fatalf("newBlobs: %v", err)
	}
	return b
}

func TestBlobs_StoreAndRetrieve(t *testing.T) {
	b := setupBlobs(t)

	content := []byte("hello world")
	expectedHash := sha256.Sum256(content)
	expectedHex := hex.EncodeToString(expectedHash[:])

	h, err := b.Store(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if h.String() != expectedHex {
		t.Errorf("Store hash = %s, want %s", h, expectedHex)
	}
	if !b.Has(h) {
		t.Error("Has() should return true after Store")
	}

	rc, err := b.Open(h)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content mismatch: got %q, want %q", got, content)
	}
}

func TestBlobs_StoreEmpty(t *testing.T) {
	b := setupBlobs(t)
	expectedHash := sha256.Sum256(nil)
	expectedHex := hex.EncodeToString(expectedHash[:])

	h, err := b.Store(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if h.String() != expectedHex {
		t.Errorf("Store hash = %s, want %s", h, expectedHex)
	}
	if !b.Has(h) {
		t.Error("Has() should return true for empty blob")
	}
}

func TestBlobs_StoreLargeContent(t *testing.T) {
	b := setupBlobs(t)
	content := bytes.Repeat([]byte("x"), 1024*1024)
	expectedHash := sha256.Sum256(content)
	expectedHex := hex.EncodeToString(expectedHash[:])

	h, err := b.Store(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if h.String() != expectedHex {
		t.Errorf("Store hash = %s, want %s", h, expectedHex)
	}
}

func TestBlobs_StoreDuplicate(t *testing.T) {
	b := setupBlobs(t)
	content := []byte("duplicate content")
	h1, err := b.Store(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store 1: %v", err)
	}
	h2, err := b.Store(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store 2: %v", err)
	}
	if !h1.Equal(h2) {
		t.Error("storing same content twice should produce same hash")
	}
}

func TestBlobs_HasNonExistent(t *testing.T) {
	b := setupBlobs(t)
	h := MustHash("0000000000000000000000000000000000000000000000000000000000000000")
	if b.Has(h) {
		t.Error("Has() should return false for non-existent blob")
	}
}

func TestBlobs_OpenNonExistent(t *testing.T) {
	b := setupBlobs(t)
	h := MustHash("0000000000000000000000000000000000000000000000000000000000000000")
	_, err := b.Open(h)
	if err == nil {
		t.Error("Open should return error for non-existent blob")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestBlobs_Path(t *testing.T) {
	b := setupBlobs(t)
	h := MustHash("9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53")
	path, err := b.Path(h)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if !strings.Contains(path, "9a") {
		t.Errorf("Path should contain prefix shard, got %q", path)
	}
	if !strings.HasSuffix(path, "09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53") {
		t.Errorf("Path should end with suffix, got %q", path)
	}
}

func TestBlobs_Delete(t *testing.T) {
	b := setupBlobs(t)
	h, err := b.Store(bytes.NewReader([]byte("to be deleted")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !b.Has(h) {
		t.Fatal("blob should exist before delete")
	}
	if err := b.Delete(h); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if b.Has(h) {
		t.Error("blob should not exist after delete")
	}
}

func TestBlobs_DeleteNonExistent(t *testing.T) {
	b := setupBlobs(t)
	h := MustHash("0000000000000000000000000000000000000000000000000000000000000000")
	if err := b.Delete(h); err == nil {
		t.Error("Delete should return error for non-existent blob")
	}
}

func TestBlobs_Iterate(t *testing.T) {
	b := setupBlobs(t)
	contents := []string{"one", "two", "three"}
	stored := make(map[string]bool)
	for _, c := range contents {
		h, err := b.Store(strings.NewReader(c))
		if err != nil {
			t.Fatalf("Store(%q): %v", c, err)
		}
		stored[h.String()] = true
	}

	found := make(map[string]bool)
	err := b.Iterate(func(h Hash) error {
		found[h.String()] = true
		return nil
	})
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if len(found) != len(stored) {
		t.Errorf("Iterate found %d blobs, want %d", len(found), len(stored))
	}
	for h := range stored {
		if !found[h] {
			t.Errorf("Iterate missed hash %s", h)
		}
	}
}

func TestBlobs_IterateEmpty(t *testing.T) {
	b := setupBlobs(t)
	count := 0
	err := b.Iterate(func(h Hash) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if count != 0 {
		t.Errorf("Iterate on empty store returned %d entries", count)
	}
}

func TestBlobs_AtomicStore(t *testing.T) {
	b := setupBlobs(t)
	h, err := b.Store(bytes.NewReader([]byte("atomic test")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	entries, err := os.ReadDir(b.root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".blob-tmp-") {
			t.Errorf("found leftover temp file: %s", entry.Name())
		}
	}
	p, err := b.Path(h)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("blob file not at expected path %s: %v", p, err)
	}
}

func TestBlobs_Sweep(t *testing.T) {
	b := setupBlobs(t)
	keepHash, _ := b.Store(strings.NewReader("keep me"))
	delHash1, _ := b.Store(strings.NewReader("delete me 1"))
	delHash2, _ := b.Store(strings.NewReader("delete me 2"))

	keep := map[Hash]struct{}{keepHash: {}}
	deleted, err := b.Sweep(keep)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if deleted != 2 {
		t.Errorf("Sweep deleted %d, want 2", deleted)
	}
	if !b.Has(keepHash) {
		t.Error("kept blob was deleted")
	}
	if b.Has(delHash1) || b.Has(delHash2) {
		t.Error("unkept blob survived sweep")
	}

	deleted, err = b.Sweep(keep)
	if err != nil {
		t.Fatalf("Sweep (idempotent): %v", err)
	}
	if deleted != 0 {
		t.Errorf("second Sweep deleted %d, want 0", deleted)
	}
}
