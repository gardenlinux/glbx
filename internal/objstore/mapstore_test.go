package objstore

import (
	"bytes"
	"strings"
	"testing"
)

func setupMapStore(t *testing.T) (*localMap, *localBlobs) {
	t.Helper()
	dir := t.TempDir()
	b, err := newBlobs(dir + "/blobs")
	if err != nil {
		t.Fatalf("newBlobs: %v", err)
	}
	m, err := newMapStore(dir+"/map", b)
	if err != nil {
		t.Fatalf("newMapStore: %v", err)
	}
	return m, b
}

func TestMapStore_SetAndGet(t *testing.T) {
	m, b := setupMapStore(t)
	blobHash, err := b.Store(bytes.NewReader([]byte("blob content")))
	if err != nil {
		t.Fatalf("Store blob: %v", err)
	}
	key := MustHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := m.Set(key, blobHash, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := m.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Equal(blobHash) {
		t.Errorf("Get = %s, want %s", got, blobHash)
	}
}

func TestMapStore_Has(t *testing.T) {
	m, b := setupMapStore(t)
	blobHash, _ := b.Store(bytes.NewReader([]byte("data")))
	key := MustHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if m.Has(key) {
		t.Error("Has() should return false before Set")
	}
	m.Set(key, blobHash, false)
	if !m.Has(key) {
		t.Error("Has() should return true after Set")
	}
}

func TestMapStore_SetValidatesBlob(t *testing.T) {
	m, _ := setupMapStore(t)
	key := MustHash("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	nonExistentBlob := MustHash("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	err := m.Set(key, nonExistentBlob, true)
	if err == nil {
		t.Error("Set with validation should fail for non-existent blob")
	}
	if !strings.Contains(err.Error(), "does not reference an existing blob") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMapStore_SetWithoutValidation(t *testing.T) {
	m, _ := setupMapStore(t)
	key := MustHash("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	value := MustHash("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if err := m.Set(key, value, false); err != nil {
		t.Fatalf("Set without validation: %v", err)
	}
	got, err := m.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Equal(value) {
		t.Errorf("Get = %s, want %s", got, value)
	}
}

func TestMapStore_GetNonExistent(t *testing.T) {
	m, _ := setupMapStore(t)
	key := MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	_, err := m.Get(key)
	if err == nil {
		t.Error("Get should return error for non-existent key")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestMapStore_Delete(t *testing.T) {
	m, b := setupMapStore(t)
	blobHash, _ := b.Store(bytes.NewReader([]byte("to delete")))
	key := MustHash("2222222222222222222222222222222222222222222222222222222222222222")
	m.Set(key, blobHash, false)
	if err := m.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m.Has(key) {
		t.Error("Has() should return false after Delete")
	}
}

func TestMapStore_DeleteNonExistent(t *testing.T) {
	m, _ := setupMapStore(t)
	key := MustHash("3333333333333333333333333333333333333333333333333333333333333333")
	if err := m.Delete(key); err == nil {
		t.Error("Delete should return error for non-existent key")
	}
}

func TestMapStore_Overwrite(t *testing.T) {
	m, b := setupMapStore(t)
	hash1, _ := b.Store(bytes.NewReader([]byte("first")))
	hash2, _ := b.Store(bytes.NewReader([]byte("second")))
	key := MustHash("4444444444444444444444444444444444444444444444444444444444444444")
	m.Set(key, hash1, false)
	m.Set(key, hash2, false)
	got, err := m.Get(key)
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if !got.Equal(hash2) {
		t.Errorf("Get = %s, want %s (overwritten value)", got, hash2)
	}
}

func TestMapStore_Iterate(t *testing.T) {
	m, b := setupMapStore(t)
	blobHash, _ := b.Store(bytes.NewReader([]byte("data")))
	keys := []Hash{
		MustHash("5555555555555555555555555555555555555555555555555555555555555555"),
		MustHash("6666666666666666666666666666666666666666666666666666666666666666"),
		MustHash("7777777777777777777777777777777777777777777777777777777777777777"),
	}
	for _, k := range keys {
		m.Set(k, blobHash, false)
	}
	found := make(map[string]bool)
	err := m.Iterate(func(key Hash) error {
		found[key.String()] = true
		return nil
	})
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if len(found) != len(keys) {
		t.Errorf("Iterate found %d keys, want %d", len(found), len(keys))
	}
	for _, k := range keys {
		if !found[k.String()] {
			t.Errorf("Iterate missed key %s", k)
		}
	}
}

func TestMapStore_IterateEmpty(t *testing.T) {
	m, _ := setupMapStore(t)
	count := 0
	err := m.Iterate(func(key Hash) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if count != 0 {
		t.Errorf("Iterate on empty map returned %d entries", count)
	}
}

func TestMapStore_SweepFollowingBlobs(t *testing.T) {
	m, b := setupMapStore(t)
	live, _ := b.Store(bytes.NewReader([]byte("live")))
	dead, _ := b.Store(bytes.NewReader([]byte("dead")))

	liveKey := MustHash("8888888888888888888888888888888888888888888888888888888888888888")
	deadKey := MustHash("9999999999999999999999999999999999999999999999999999999999999999")
	m.Set(liveKey, live, true)
	m.Set(deadKey, dead, true)

	// Drop the blob the dead entry points at, then sweep the map.
	if err := b.Delete(dead); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	deleted, err := m.SweepFollowingBlobs()
	if err != nil {
		t.Fatalf("SweepFollowingBlobs: %v", err)
	}
	if deleted != 1 {
		t.Errorf("swept %d entries, want 1", deleted)
	}
	if !m.Has(liveKey) {
		t.Error("entry with a live blob should survive")
	}
	if m.Has(deadKey) {
		t.Error("entry whose blob is gone should be swept")
	}
}
