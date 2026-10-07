package objstore

import (
	"fmt"
	"io"
)

// MapAlias is one artifact identity to publish as a map-<id> tag, paired with
// the manifest-blob hash its value points at.
type MapAlias struct {
	Identity     Hash
	ManifestHash Hash
}

// PublishResult summarizes a publish run.
type PublishResult struct {
	BlobsUploaded  int // blobs pushed and tagged this run
	BlobsPresent   int // blobs already on the registry
	BlobsMissing   int // enumerated but absent from the local store, skipped
	Aliases        int // map-<id> tags set this run
	AliasesPresent int // map-<id> tags already present
}

// Publisher fills a registry from a local store. It is constructed against a
// registry reference and reads blob bytes from the local blob store it is given.
type Publisher struct {
	reg   *ociRegistry
	local BlobStore
}

// NewPublisher builds a publisher targeting the OCI registry at ref
// ("host[:port]/repo"), reading content from local. insecure selects plaintext
// HTTP for a local test registry.
func NewPublisher(ref string, insecure bool, local BlobStore) (*Publisher, error) {
	reg, err := parseRegistry(ref, insecure)
	if err != nil {
		return nil, err
	}
	return &Publisher{reg: reg, local: local}, nil
}

// EnsureBlob publishes one blob if the registry lacks it. A blob the local store
// does not hold is reported absent and skipped — publishing mirrors what the
// local store holds. The return reports whether an upload happened and whether
// the blob was missing locally.
func (p *Publisher) EnsureBlob(h Hash) (uploaded, missing bool, err error) {
	exists, err := p.reg.tagExists(blobTag(h))
	if err != nil {
		return false, false, fmt.Errorf("check blob %s: %w", h, err)
	}
	if exists {
		return false, false, nil
	}

	data, err := p.readLocal(h)
	if err != nil {
		return false, true, nil
	}
	if err := p.reg.publishBlob(h, data); err != nil {
		return false, false, err
	}
	return true, false, nil
}

// EnsureAlias sets the map-<id> tag if absent, pointing it at the byte-identical
// one-layer manifest that wraps the manifest blob. The manifest blob must have
// been ensured first; its bytes are read locally to size the layer descriptor.
func (p *Publisher) EnsureAlias(a MapAlias) (set bool, err error) {
	exists, err := p.reg.tagExists(mapTag(a.Identity))
	if err != nil {
		return false, fmt.Errorf("check alias %s: %w", a.Identity, err)
	}
	if exists {
		return false, nil
	}
	data, err := p.readLocal(a.ManifestHash)
	if err != nil {
		return false, fmt.Errorf("alias %s: manifest blob %s not local: %w", a.Identity, a.ManifestHash, err)
	}
	if err := p.reg.aliasMap(a.Identity, a.ManifestHash, int64(len(data))); err != nil {
		return false, err
	}
	return true, nil
}

// Publish mirrors a worklist to the registry: ensure every blob hash, then set
// every map alias. It is idempotent — a second run over an unchanged worklist
// uploads nothing. A blob absent from the local store is skipped, not fatal.
func (p *Publisher) Publish(blobs []Hash, aliases []MapAlias) (PublishResult, error) {
	var res PublishResult
	for _, h := range blobs {
		uploaded, missing, err := p.EnsureBlob(h)
		switch {
		case err != nil:
			return res, err
		case missing:
			res.BlobsMissing++
		case uploaded:
			res.BlobsUploaded++
		default:
			res.BlobsPresent++
		}
	}
	for _, a := range aliases {
		set, err := p.EnsureAlias(a)
		if err != nil {
			return res, err
		}
		if set {
			res.Aliases++
		} else {
			res.AliasesPresent++
		}
	}
	return res, nil
}

// readLocal reads a blob's full bytes from the local store.
func (p *Publisher) readLocal(h Hash) ([]byte, error) {
	rc, err := p.local.Open(h)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
