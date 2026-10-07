package objstore

import (
	"fmt"
	"io"
)

// pullThroughBlobs is a BlobStore that satisfies a read miss from a remote
// BlobStore, writing the pulled bytes into the local layer so the next read is a
// local hit. Materialization is lazy: it happens when the bytes are actually
// read (Open/Path), not on a mere existence check (Has). Writes and maintenance
// act on the local layer only.
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

// Has reports existence without materializing: a blob present locally or on the
// remote (a remote existence check is a cheap HEAD) answers true, but the bytes
// are not pulled. A caller that then needs the content calls Open or Path, which
// materialize it. This keeps an existence probe from dragging the blob to disk.
func (p *pullThroughBlobs) Has(h Hash) bool {
	return p.local.Has(h) || p.remote.Has(h)
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
// MapStore. On a local miss it resolves the identity to the hash it points at,
// materializes that one blob locally, and records the local entry — turning the
// miss into a permanent local hit. It treats the pointed-at hash opaquely: it
// does not read the blob or know what the value refers to. Writes and
// maintenance act on the local layer only.
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

	value, err := p.remote.Get(key)
	if err != nil {
		// A remote miss falls back to the local not-found; a transport error
		// surfaces so a genuine failure is not mistaken for an absent entry.
		if isNotExist(err) {
			return p.local.Get(key)
		}
		return Hash{}, err
	}

	// Materialize the pointed-at blob locally, verifying its bytes against the
	// hash, so the validated local Set below can reference it. What the blob
	// contains is the caller's concern, not the map's.
	rc, err := p.blobs.Open(value)
	if err != nil {
		return Hash{}, fmt.Errorf("pull map value %s: %w", value, err)
	}
	rc.Close()

	if err := p.local.Set(key, value, true); err != nil {
		return Hash{}, fmt.Errorf("set map after pull-through: %w", err)
	}
	return value, nil
}

func (p *pullThroughMap) Set(key, value Hash, validate bool) error {
	return p.local.Set(key, value, validate)
}
func (p *pullThroughMap) Delete(key Hash) error                 { return p.local.Delete(key) }
func (p *pullThroughMap) Iterate(fn func(key Hash) error) error { return p.local.Iterate(fn) }
func (p *pullThroughMap) SweepFollowingBlobs() (int, error)     { return p.local.SweepFollowingBlobs() }
