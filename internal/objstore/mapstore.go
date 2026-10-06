package objstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MapStore maps an artifact identity to the hash of its manifest blob. A
// pull-through implementation reconstructs an absent entry from a remote on read;
// a local implementation answers from the filesystem alone. Write and maintenance
// operations always act on the local layer.
type MapStore interface {
	Has(key Hash) bool
	Get(key Hash) (Hash, error)
	Set(key, value Hash, validate bool) error
	Delete(key Hash) error
	Iterate(fn func(key Hash) error) error
	SweepFollowingBlobs() (int, error)
}

// localMap is the filesystem-backed MapStore. Each entry is a one-line file at
// <root>/<prefix>/<suffix> holding the manifest-blob hash. It has no knowledge of
// any remote.
type localMap struct {
	root  string
	blobs BlobStore
}

func newMapStore(root string, blobs BlobStore) (*localMap, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating map directory: %w", err)
	}
	return &localMap{root: root, blobs: blobs}, nil
}

func (m *localMap) path(key Hash) string {
	return filepath.Join(m.root, key.Prefix(), key.Suffix())
}

// Has reports whether a mapping exists for key.
func (m *localMap) Has(key Hash) bool {
	_, err := os.Stat(m.path(key))
	return err == nil
}

// Get returns the blob hash key maps to, or an error if the mapping is absent.
func (m *localMap) Get(key Hash) (Hash, error) {
	data, err := os.ReadFile(m.path(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Hash{}, fmt.Errorf("map key %s not found", key)
		}
		return Hash{}, fmt.Errorf("reading map entry %s: %w", key, err)
	}
	h, err := NewHash(strings.TrimSpace(string(data)))
	if err != nil {
		return Hash{}, fmt.Errorf("invalid hash in map entry %s: %w", key, err)
	}
	return h, nil
}

// Set records a mapping from key to value, written atomically. When validate is
// true it first checks that value references an existing blob.
func (m *localMap) Set(key, value Hash, validate bool) error {
	if validate && !m.blobs.Has(value) {
		return fmt.Errorf("map value %s does not reference an existing blob", value)
	}

	shardDir := filepath.Join(m.root, key.Prefix())
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		return fmt.Errorf("creating map shard directory: %w", err)
	}

	tmp, err := os.CreateTemp(shardDir, ".map-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file for map entry: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := fmt.Fprintf(tmp, "%s\n", value); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing map entry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp map file: %w", err)
	}
	if err := os.Rename(tmpPath, m.path(key)); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming map entry to final path: %w", err)
	}
	return nil
}

// Delete removes a mapping by key, erroring if it is absent.
func (m *localMap) Delete(key Hash) error {
	p := m.path(key)
	if err := os.Remove(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("map key %s not found", key)
		}
		return fmt.Errorf("deleting map entry %s: %w", key, err)
	}
	os.Remove(filepath.Join(m.root, key.Prefix()))
	return nil
}

// Iterate calls fn for each map key. Iteration stops on the first error fn
// returns.
func (m *localMap) Iterate(fn func(key Hash) error) error {
	shards, err := os.ReadDir(m.root)
	if err != nil {
		return fmt.Errorf("reading map directory: %w", err)
	}
	for _, shard := range shards {
		if !shard.IsDir() || len(shard.Name()) != 2 {
			continue
		}
		shardPath := filepath.Join(m.root, shard.Name())
		entries, err := os.ReadDir(shardPath)
		if err != nil {
			return fmt.Errorf("reading map shard directory %s: %w", shard.Name(), err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			h, err := NewHash(shard.Name() + entry.Name())
			if err != nil {
				continue
			}
			if err := fn(h); err != nil {
				return err
			}
		}
	}
	return nil
}

// SweepFollowingBlobs deletes every map entry whose target blob no longer
// exists, keeping the map consistent with the blobs after a blob sweep.
func (m *localMap) SweepFollowingBlobs() (int, error) {
	var dead []Hash
	err := m.Iterate(func(key Hash) error {
		target, err := m.Get(key)
		if err != nil || !m.blobs.Has(target) {
			dead = append(dead, key)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, key := range dead {
		if err := m.Delete(key); err != nil {
			return deleted, fmt.Errorf("map sweep: %w", err)
		}
		deleted++
	}
	return deleted, nil
}
