package objstore

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// defaultPublishConcurrency bounds the in-flight existence probes / uploads a
// Publish run issues at once. It matches restore's default (internal/restore):
// the warm path is dominated by one registry round-trip per worklist item, and
// those probes are independent and idempotent, so fanning them out turns N serial
// round-trips into ~N/concurrency.
const defaultPublishConcurrency = 16

// MapAlias is one map entry to publish as a map-<key> tag, paired with the blob
// hash its value points at. Both identity→manifest and log-key→log-blob entries
// are published this way.
type MapAlias struct {
	Key   Hash
	Value Hash
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

	// Concurrency bounds the number of registry round-trips a Publish issues at
	// once. Zero means defaultPublishConcurrency. Callers may set it after
	// construction to tune the fan-out.
	Concurrency int
}

// NewPublisher builds a publisher targeting the OCI registry at ref
// ("host[:port]/repo"), reading content from local. insecure selects plaintext
// HTTP for a local test registry.
func NewPublisher(ref string, insecure bool, local BlobStore) (*Publisher, error) {
	reg, err := parseRegistry(ref, insecure)
	if err != nil {
		return nil, err
	}
	return &Publisher{reg: reg, local: local, Concurrency: defaultPublishConcurrency}, nil
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

// EnsureAlias sets the map-<key> tag if absent, pointing it at the exact
// manifest blob-<value> already names so the two resolve to the same digest.
// The value blob must have been published first — every publish path ensures
// the alias's value is in the blob worklist uploaded ahead of the aliases — so
// this reads nothing locally and works on a cold runner against blobs a prior
// run put up.
func (p *Publisher) EnsureAlias(a MapAlias) (set bool, err error) {
	exists, err := p.reg.tagExists(mapTag(a.Key))
	if err != nil {
		return false, fmt.Errorf("check alias %s: %w", a.Key, err)
	}
	if exists {
		return false, nil
	}
	if err := p.reg.aliasMap(a.Key, a.Value); err != nil {
		return false, err
	}
	return true, nil
}

// PublishProgress observes a Publish run item by item. done counts items
// finished so far (1-based) against total, kind is "blob" or "alias", ref names
// the item (hash or identity), and outcome is a short word: "uploaded",
// "present", "missing", "set". Publish runs items concurrently, so the callback
// is serialized under a lock (never entered concurrently) and done increases
// monotonically, but items no longer arrive in worklist order. It must not block
// for long. A nil progress is ignored.
type PublishProgress func(done, total int, kind, ref, outcome string)

// Publish mirrors a worklist to the registry: ensure every blob hash, then set
// every map alias. It is idempotent — a second run over an unchanged worklist
// uploads nothing. A blob absent from the local store is skipped, not fatal.
// progress, when non-nil, is invoked once per item for live reporting.
//
// Items run concurrently, bounded by p.Concurrency. All blobs complete before
// any alias is attempted: EnsureAlias assumes its value blob was already
// published (the published-blobs-first contract), so the two phases must not
// overlap. Within a phase items are independent. The first error from any item
// is returned (joined); a real error fails the whole run, matching the prior
// serial behavior.
func (p *Publisher) Publish(blobs []Hash, aliases []MapAlias, progress PublishProgress) (PublishResult, error) {
	var res PublishResult
	total := len(blobs) + len(aliases)

	concurrency := p.Concurrency
	if concurrency <= 0 {
		concurrency = defaultPublishConcurrency
	}

	var (
		blobsUploaded, blobsPresent, blobsMissing atomic.Int32
		aliasesSet, aliasesPresent                atomic.Int32
		done                                      atomic.Int32
		progressMu                                sync.Mutex
	)

	report := func(kind, ref, outcome string) {
		if progress == nil {
			return
		}
		d := int(done.Add(1))
		progressMu.Lock()
		progress(d, total, kind, ref, outcome)
		progressMu.Unlock()
	}

	// runPhase fans n units of work out over a bounded pool, collecting a
	// per-index error so a failure is reported without a data race. Mirrors the
	// worker-pool shape in internal/restore.
	runPhase := func(n int, work func(i int) error) error {
		errs := make([]error, n)
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				errs[i] = work(i)
			}(i)
		}
		wg.Wait()
		return errors.Join(errs...)
	}

	// Phase 1: every blob.
	if err := runPhase(len(blobs), func(i int) error {
		h := blobs[i]
		uploaded, missing, err := p.EnsureBlob(h)
		if err != nil {
			return err
		}
		switch {
		case missing:
			blobsMissing.Add(1)
			report("blob", h.Short(), "missing")
		case uploaded:
			blobsUploaded.Add(1)
			report("blob", h.Short(), "uploaded")
		default:
			blobsPresent.Add(1)
			report("blob", h.Short(), "present")
		}
		return nil
	}); err != nil {
		res.BlobsUploaded = int(blobsUploaded.Load())
		res.BlobsPresent = int(blobsPresent.Load())
		res.BlobsMissing = int(blobsMissing.Load())
		return res, err
	}

	// Phase 2: every alias, only after all blobs are published.
	if err := runPhase(len(aliases), func(i int) error {
		a := aliases[i]
		set, err := p.EnsureAlias(a)
		if err != nil {
			return err
		}
		if set {
			aliasesSet.Add(1)
			report("alias", a.Key.Short(), "set")
		} else {
			aliasesPresent.Add(1)
			report("alias", a.Key.Short(), "present")
		}
		return nil
	}); err != nil {
		res.BlobsUploaded = int(blobsUploaded.Load())
		res.BlobsPresent = int(blobsPresent.Load())
		res.BlobsMissing = int(blobsMissing.Load())
		res.Aliases = int(aliasesSet.Load())
		res.AliasesPresent = int(aliasesPresent.Load())
		return res, err
	}

	res.BlobsUploaded = int(blobsUploaded.Load())
	res.BlobsPresent = int(blobsPresent.Load())
	res.BlobsMissing = int(blobsMissing.Load())
	res.Aliases = int(aliasesSet.Load())
	res.AliasesPresent = int(aliasesPresent.Load())
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
