package objstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Blobs is content-addressed blob storage. A blob lives at
// <root>/<prefix>/<suffix>, derived from the SHA-256 of its bytes, so the same
// content always maps to the same path and a blob is immutable under its
// address.
type Blobs struct {
	root string
}

func newBlobs(root string) (*Blobs, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating blobs directory: %w", err)
	}
	return &Blobs{root: root}, nil
}

func (b *Blobs) path(h Hash) string {
	return filepath.Join(b.root, h.Prefix(), h.Suffix())
}

// Has reports whether a blob with the given hash exists.
func (b *Blobs) Has(h Hash) bool {
	_, err := os.Stat(b.path(h))
	return err == nil
}

// Path returns the filesystem path a blob would occupy. It does not check that
// the blob exists.
func (b *Blobs) Path(h Hash) string {
	return b.path(h)
}

// Open returns a reader over the blob's bytes, or an error if it is absent.
func (b *Blobs) Open(h Hash) (io.ReadCloser, error) {
	p := b.path(h)
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("blob %s not found", h)
		}
		if errors.Is(err, os.ErrPermission) {
			if chErr := os.Chmod(p, 0o644); chErr == nil {
				if f, err = os.Open(p); err == nil {
					return f, nil
				}
			}
			return nil, fmt.Errorf("opening blob %s: %w", h, err)
		}
		return nil, fmt.Errorf("opening blob %s: %w", h, err)
	}
	return f, nil
}

// Store streams r to a temporary file while hashing it, then renames the file
// into its content-addressed place once the digest is known. A reader never
// sees a partial blob under a valid address, and concurrent writers of the same
// content converge on the same path. Returns the content hash.
func (b *Blobs) Store(r io.Reader) (Hash, error) {
	tmp, err := os.CreateTemp(b.root, ".blob-tmp-*")
	if err != nil {
		return Hash{}, fmt.Errorf("creating temp file for blob: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), r); err != nil {
		return Hash{}, fmt.Errorf("writing blob content: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Hash{}, fmt.Errorf("closing temp blob file: %w", err)
	}

	h := Hash{hex: hex.EncodeToString(hasher.Sum(nil))}

	shardDir := filepath.Join(b.root, h.Prefix())
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		return Hash{}, fmt.Errorf("creating shard directory: %w", err)
	}

	finalPath := b.path(h)
	if _, err := os.Stat(finalPath); err == nil {
		os.Remove(tmpPath)
		success = true
		return h, nil
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return Hash{}, fmt.Errorf("renaming blob to final path: %w", err)
	}
	os.Chmod(finalPath, 0o644)

	success = true
	return h, nil
}

// Delete removes a blob by hash, erroring if it is absent.
func (b *Blobs) Delete(h Hash) error {
	p := b.path(h)
	if err := os.Remove(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("blob %s not found", h)
		}
		return fmt.Errorf("deleting blob %s: %w", h, err)
	}
	os.Remove(filepath.Join(b.root, h.Prefix()))
	return nil
}

// Sweep deletes every blob not present in keep and returns the count deleted.
// The caller supplies the keep-set; Sweep applies no policy of its own.
func (b *Blobs) Sweep(keep map[Hash]struct{}) (int, error) {
	var toDelete []Hash
	err := b.Iterate(func(h Hash) error {
		if _, ok := keep[h]; !ok {
			toDelete = append(toDelete, h)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, h := range toDelete {
		if err := b.Delete(h); err != nil {
			return deleted, fmt.Errorf("sweep: %w", err)
		}
		deleted++
	}
	return deleted, nil
}

// Iterate calls fn for each stored blob hash. Iteration stops on the first
// error fn returns.
func (b *Blobs) Iterate(fn func(Hash) error) error {
	shards, err := os.ReadDir(b.root)
	if err != nil {
		return fmt.Errorf("reading blobs directory: %w", err)
	}
	for _, shard := range shards {
		if !shard.IsDir() || len(shard.Name()) != 2 {
			continue
		}
		shardPath := filepath.Join(b.root, shard.Name())
		entries, err := os.ReadDir(shardPath)
		if err != nil {
			return fmt.Errorf("reading shard directory %s: %w", shard.Name(), err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			h, err := NewHash(shard.Name() + entry.Name())
			if err != nil {
				continue // skip temp files and other non-blob names
			}
			if err := fn(h); err != nil {
				return err
			}
		}
	}
	return nil
}
