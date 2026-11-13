package objstore

import (
	"strings"
	"sync"
	"testing"
)

// TestPublishConcurrentCounts drives Publish over a mixed worklist with
// Concurrency > 1 and checks the PublishResult tallies every category, that a
// second run is a pure no-op (idempotent), and that the progress callback is
// invoked exactly once per item with a monotone, correctly-bounded done count.
func TestPublishConcurrentCounts(t *testing.T) {
	reg, _ := newTestRegistry(t)
	local := newLocalForTest(t)

	// New blobs: present locally, absent on the registry → should upload.
	var newBlobs []Hash
	for i := 0; i < 20; i++ {
		h, err := local.Store(strings.NewReader("new-blob-" + string(rune('a'+i))))
		if err != nil {
			t.Fatalf("store new blob %d: %v", i, err)
		}
		newBlobs = append(newBlobs, h)
	}

	// Present blobs: already on the registry (and locally) → should count present.
	var presentBlobs []Hash
	for i := 0; i < 15; i++ {
		content := []byte("present-blob-" + string(rune('a'+i)))
		h, err := local.Store(strings.NewReader(string(content)))
		if err != nil {
			t.Fatalf("store present blob %d: %v", i, err)
		}
		if err := reg.publishBlob(h, content); err != nil {
			t.Fatalf("pre-publish blob %d: %v", i, err)
		}
		presentBlobs = append(presentBlobs, h)
	}

	// Missing blobs: absent from the local store → should count missing, skipped.
	missingBlobs := []Hash{
		MustHash("1111111111111111111111111111111111111111111111111111111111111111"),
		MustHash("2222222222222222222222222222222222222222222222222222222222222222"),
		MustHash("3333333333333333333333333333333333333333333333333333333333333333"),
	}

	// Aliases: each points at an already-published value blob. Build them over the
	// present blobs (already on the registry) so the published-blobs-first contract
	// holds regardless of phase-1 upload ordering.
	var aliases []MapAlias
	for i, v := range presentBlobs[:10] {
		key := MustHash(strings.Repeat(string("0123456789abcdef"[i]), 64))
		aliases = append(aliases, MapAlias{Key: key, Value: v})
	}

	var blobs []Hash
	blobs = append(blobs, newBlobs...)
	blobs = append(blobs, presentBlobs...)
	blobs = append(blobs, missingBlobs...)
	total := len(blobs) + len(aliases)

	pub, err := NewPublisher("http://"+regRef(reg), false, local)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	pub.Concurrency = 8

	var (
		mu       sync.Mutex
		calls    int
		seenDone = map[int]bool{}
		maxDone  int
		badTotal bool
		badOrNeg bool
	)

	prog := func(done, tot int, kind, ref, outcome string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if tot != total {
			badTotal = true
		}
		if done < 1 || done > total {
			badOrNeg = true
		}
		if seenDone[done] {
			badOrNeg = true // duplicate done value → not monotone/unique
		}
		seenDone[done] = true
		if done > maxDone {
			maxDone = done
		}
	}

	res, err := pub.Publish(blobs, aliases, prog)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if res.BlobsUploaded != len(newBlobs) {
		t.Errorf("BlobsUploaded = %d, want %d", res.BlobsUploaded, len(newBlobs))
	}
	if res.BlobsPresent != len(presentBlobs) {
		t.Errorf("BlobsPresent = %d, want %d", res.BlobsPresent, len(presentBlobs))
	}
	if res.BlobsMissing != len(missingBlobs) {
		t.Errorf("BlobsMissing = %d, want %d", res.BlobsMissing, len(missingBlobs))
	}
	if res.Aliases != len(aliases) {
		t.Errorf("Aliases = %d, want %d", res.Aliases, len(aliases))
	}
	if res.AliasesPresent != 0 {
		t.Errorf("AliasesPresent = %d, want 0", res.AliasesPresent)
	}

	if calls != total {
		t.Errorf("progress called %d times, want %d", calls, total)
	}
	if badTotal {
		t.Error("progress received a wrong total")
	}
	if badOrNeg {
		t.Error("progress done was out of range or duplicated")
	}
	if maxDone != total {
		t.Errorf("max progress done = %d, want %d", maxDone, total)
	}

	// Second run: everything is now present on the registry → a pure no-op.
	res2, err := pub.Publish(blobs, aliases, nil)
	if err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if res2.BlobsUploaded != 0 {
		t.Errorf("second run BlobsUploaded = %d, want 0", res2.BlobsUploaded)
	}
	if res2.BlobsPresent != len(newBlobs)+len(presentBlobs) {
		t.Errorf("second run BlobsPresent = %d, want %d", res2.BlobsPresent, len(newBlobs)+len(presentBlobs))
	}
	if res2.BlobsMissing != len(missingBlobs) {
		t.Errorf("second run BlobsMissing = %d, want %d", res2.BlobsMissing, len(missingBlobs))
	}
	if res2.Aliases != 0 {
		t.Errorf("second run Aliases = %d, want 0", res2.Aliases)
	}
	if res2.AliasesPresent != len(aliases) {
		t.Errorf("second run AliasesPresent = %d, want %d", res2.AliasesPresent, len(aliases))
	}
}
