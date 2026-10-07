package objstore

import (
	"fmt"
	"io"
)

// pullThroughBlobs is a BlobStore that satisfies a read miss from a remote
// BlobStore, writing the pulled bytes into the local layer so the next read is a
// local hit. Writes and maintenance act on the local layer only.
type pullThroughBlobs struct {
	local  BlobStore
	remote BlobStore
}

// ensure materializes h locally if absent, pulling it from the remote. It is
// best-effort: a remote miss or a digest mismatch leaves the local layer as-is,
// and the subsequent local read reports the canonical not-found. A mismatched
// pull is stored under its actual hash (never the requested one) and left for
// garbage collection.
func (p *pullThroughBlobs) ensure(h Hash) {
	if p.local.Has(h) {
		return
	}
	rc, err := p.remote.Open(h)
	if err != nil {
		return
	}
	defer rc.Close()
	_, _ = p.local.Store(rc)
}

func (p *pullThroughBlobs) Has(h Hash) bool {
	p.ensure(h)
	return p.local.Has(h)
}

func (p *pullThroughBlobs) Open(h Hash) (io.ReadCloser, error) {
	p.ensure(h)
	return p.local.Open(h)
}

func (p *pullThroughBlobs) Path(h Hash) (string, error) {
	p.ensure(h)
	return p.local.Path(h)
}

func (p *pullThroughBlobs) Store(r io.Reader) (Hash, error) { return p.local.Store(r) }
func (p *pullThroughBlobs) Delete(h Hash) error             { return p.local.Delete(h) }
func (p *pullThroughBlobs) Sweep(keep map[Hash]struct{}) (int, error) {
	return p.local.Sweep(keep)
}
func (p *pullThroughBlobs) Iterate(fn func(Hash) error) error { return p.local.Iterate(fn) }

// pullThroughMap is a MapStore that reconstructs an absent entry from a remote
// MapStore. On a local miss it resolves the identity to its manifest-blob hash,
// pulls that manifest blob through blobs, reads it to recover the output list,
// pulls each output blob through blobs, and records the local entry pointing at
// the now-present manifest blob — turning the miss into a permanent local hit.
// Writes and maintenance act on the local layer only.
type pullThroughMap struct {
	local  MapStore
	blobs  BlobStore
	remote MapStore
}

func (p *pullThroughMap) Has(key Hash) bool { return p.local.Has(key) }

func (p *pullThroughMap) Get(key Hash) (Hash, error) {
	if h, err := p.local.Get(key); err == nil {
		return h, nil
	}

	manifestHash, err := p.remote.Get(key)
	if err != nil {
		// A remote miss falls back to the local not-found; a transport error
		// surfaces so a genuine failure is not mistaken for an absent entry.
		if isNotExist(err) {
			return p.local.Get(key)
		}
		return Hash{}, err
	}

	// Pulling the manifest blob through blobs materializes it locally and
	// verifies its bytes against manifestHash.
	rc, err := p.blobs.Open(manifestHash)
	if err != nil {
		return Hash{}, fmt.Errorf("pull manifest blob %s: %w", manifestHash, err)
	}
	outputs, err := ParseManifest(rc)
	rc.Close()
	if err != nil {
		return Hash{}, fmt.Errorf("parse pulled manifest %s: %w", manifestHash, err)
	}

	for _, out := range outputs {
		if !p.blobs.Has(out.Hash) {
			return Hash{}, fmt.Errorf("pull output %s: not available", out.Hash)
		}
	}

	if err := p.local.Set(key, manifestHash, true); err != nil {
		return Hash{}, fmt.Errorf("set map after pull-through: %w", err)
	}
	return manifestHash, nil
}

func (p *pullThroughMap) Set(key, value Hash, validate bool) error {
	return p.local.Set(key, value, validate)
}
func (p *pullThroughMap) Delete(key Hash) error                 { return p.local.Delete(key) }
func (p *pullThroughMap) Iterate(fn func(key Hash) error) error { return p.local.Iterate(fn) }
func (p *pullThroughMap) SweepFollowingBlobs() (int, error)     { return p.local.SweepFollowingBlobs() }
