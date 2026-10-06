package objstore

import (
	"fmt"
	"io"
	"strings"
)

// pullThroughBlobs is a BlobStore that satisfies a read miss from a remote,
// writing the pulled bytes into the local layer so the next read is a local hit.
// Writes and maintenance act on the local layer only.
type pullThroughBlobs struct {
	local  BlobStore
	remote Remote
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
	rc, err := p.remote.OpenBlob(h)
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

// pullThroughMap is a MapStore that reconstructs an absent entry from a remote.
// On a local miss it fetches the identity's leaf outputs, pulls each leaf blob
// through blobs, rebuilds the manifest blob byte-identically, stores it, and
// records the entry — turning the miss into a permanent local hit. Writes and
// maintenance act on the local layer only.
type pullThroughMap struct {
	local  MapStore
	blobs  BlobStore
	remote Remote
}

func (p *pullThroughMap) Has(key Hash) bool { return p.local.Has(key) }

func (p *pullThroughMap) Get(key Hash) (Hash, error) {
	if h, err := p.local.Get(key); err == nil {
		return h, nil
	}

	leaves, ok, err := p.remote.ManifestLeaves(key)
	if err != nil {
		return Hash{}, err
	}
	if !ok {
		return p.local.Get(key)
	}

	for _, leaf := range leaves {
		if !p.blobs.Has(leaf.Hash) {
			return Hash{}, fmt.Errorf("pull leaf %s: not available", leaf.Hash)
		}
	}

	manifestHash, err := p.blobs.Store(strings.NewReader(SerializeManifest(leaves)))
	if err != nil {
		return Hash{}, fmt.Errorf("store reconstructed manifest: %w", err)
	}
	if err := p.local.Set(key, manifestHash, true); err != nil {
		return Hash{}, fmt.Errorf("set map after pull-through: %w", err)
	}
	return manifestHash, nil
}

func (p *pullThroughMap) Set(key, value Hash, validate bool) error {
	return p.local.Set(key, value, validate)
}
func (p *pullThroughMap) Delete(key Hash) error                  { return p.local.Delete(key) }
func (p *pullThroughMap) Iterate(fn func(key Hash) error) error  { return p.local.Iterate(fn) }
func (p *pullThroughMap) SweepFollowingBlobs() (int, error)      { return p.local.SweepFollowingBlobs() }
