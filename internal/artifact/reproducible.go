package artifact

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// ReproResult is one artifact's reproducibility outcome. OriginalManifest is the
// manifest-blob hash the cache already holds; ReproducedManifest is the hash of
// the manifest produced by rebuilding the node from its original inputs. Match is
// true when the two are equal and the rebuild succeeded. Log is the hash of the
// rebuild's log blob — stored blob-only, with no map entry, so this field is the
// only way to reach it. Err is set when the rebuild itself failed.
type ReproResult struct {
	Key                string
	ShortName          string
	OriginalManifest   objstore.Hash
	ReproducedManifest objstore.Hash
	Log                objstore.Hash
	Match              bool
	Err                error
}

// ReproHooks observes the reproducibility run so a caller can drive a live UI
// without the runner importing one. NodeStarted runs before a node's rebuild and
// returns a context whose log target is the node's log buffer; the runner emits
// the build and compare lines through it. NodeFinished runs after the rebuild
// with the node's result. Both may be nil.
type ReproHooks struct {
	NodeStarted  func(key string) context.Context
	NodeFinished func(r ReproResult)
}

// checkTarget holds a single node to re-verify: the artifact, its original
// manifest hash, and its inputs resolved from the ORIGINAL cached outputs of its
// dependencies. Resolving inputs up front, before any rebuild, is what makes the
// run flat: a node never waits on another node's rebuild because every input it
// needs already exists in the store.
type checkTarget struct {
	artifact Artifact
	original objstore.Hash
	inputs   map[string]objstore.Hash
}

// CheckReproducible rebuilds each target independently and compares the rebuilt
// output manifest against the one already in the cache. Targets are the Keys to
// check; a node whose output manifest is not already in the store is a hard error
// (the check verifies an existing build, it does not create one). The run is
// flat and bounded by jobs: every node's inputs come from its dependencies'
// original cached outputs, never from a freshly rebuilt one, so a non-reproducible
// dependency cannot cascade into spurious mismatches downstream.
//
// Each rebuild's log is stored as a blob with no map entry; the returned
// ReproResult.Log is the only reference to it. No manifest, log-key, or run-record
// map entry is written, so a reproducibility run never perturbs what the normal
// build and view-logs paths resolve. Differing output blobs land in the store via
// the artifact's own Build (content-addressed: identical bytes are a no-op,
// differing bytes add a new blob without overwriting the original).
func CheckReproducible(ctx context.Context, g *Graph, store *objstore.Store, targets []string, jobs int, hooks ReproHooks) ([]ReproResult, error) {
	if jobs <= 0 {
		jobs = 1
	}
	if err := g.Build(); err != nil {
		return nil, err
	}

	// Resolve every target's original manifest and inputs before rebuilding, so a
	// missing prerequisite fails the whole run up front rather than mid-flight.
	checks := make([]checkTarget, 0, len(targets))
	for _, key := range targets {
		a := g.Find(key)
		if a == nil {
			return nil, fmt.Errorf("no artifact with Key %q in graph", key)
		}
		identity, err := a.Identity()
		if err != nil {
			return nil, fmt.Errorf("compute identity for %s: %w", key, err)
		}
		original, err := store.Map.Get(identity)
		if err != nil {
			return nil, fmt.Errorf("%s is not built; build it before checking reproducibility: %w", key, err)
		}
		inputs, err := resolveOriginalInputs(a, store)
		if err != nil {
			return nil, fmt.Errorf("resolve inputs for %s: %w", key, err)
		}
		checks = append(checks, checkTarget{artifact: a, original: original, inputs: inputs})
	}

	results := make([]ReproResult, len(checks))
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for i := range checks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = checkOne(ctx, store, checks[i], hooks)
		}(i)
	}
	wg.Wait()

	return results, nil
}

// checkOne rebuilds a single target and compares its output manifest against the
// original. It routes the rebuild's log through the node context the start hook
// returns, appends the comparison as that log's last line, and stores the log as
// a blob.
func checkOne(ctx context.Context, store *objstore.Store, c checkTarget, hooks ReproHooks) ReproResult {
	key := c.artifact.Key()
	res := ReproResult{Key: key, ShortName: c.artifact.ShortName(), OriginalManifest: c.original}

	nodeCtx := ctx
	if hooks.NodeStarted != nil {
		nodeCtx = hooks.NodeStarted(key)
	}
	l := log.From(nodeCtx, log.Reprod)

	outputs, err := c.artifact.Build(BuildContext{Ctx: nodeCtx, Store: store, Inputs: c.inputs})
	if err != nil {
		l.Error("%s: rebuild FAILED: %v", c.artifact.ShortName(), err)
		res.Err = err
	} else {
		// Store the reproduced manifest as a blob (no map entry, as the whole run
		// writes none) so the hash recorded in the report resolves to a real blob
		// and the reproduced output listing can be inspected the same way the
		// original manifest is. The stored hash equals HashBytes of the same bytes.
		manifest := SerializeManifest(outputs)
		reproduced, serr := store.Blobs.Store(strings.NewReader(manifest))
		if serr != nil {
			l.Error("%s: store reproduced manifest: %v", c.artifact.ShortName(), serr)
			res.Err = serr
		} else {
			res.ReproducedManifest = reproduced
			res.Match = res.ReproducedManifest == c.original
			if res.Match {
				l.Info("%s: output matches (%s)", c.artifact.ShortName(), res.ReproducedManifest)
			} else {
				l.Info("%s: output MISMATCH original=%s reproduced=%s", c.artifact.ShortName(), c.original, res.ReproducedManifest)
			}
		}
	}

	if buf, ok := log.BufferFrom(nodeCtx); ok {
		if h, serr := storeLogBlob(store, buf); serr == nil {
			res.Log = h
		}
	}

	if hooks.NodeFinished != nil {
		hooks.NodeFinished(res)
	}
	return res
}

// resolveOriginalInputs maps each of an artifact's Input.Name to the blob hash of
// the correspondingly named output in the input source's ORIGINAL, already-built
// manifest. It mirrors Engine.resolveInputs but reads committed manifests from the
// store rather than in-memory node outputs, so it needs no build-order edges.
func resolveOriginalInputs(a Artifact, store *objstore.Store) (map[string]objstore.Hash, error) {
	inputs := a.Inputs()
	if len(inputs) == 0 {
		return nil, nil
	}
	resolved := make(map[string]objstore.Hash, len(inputs))
	for _, input := range inputs {
		outputs, err := ResolveOutputs(input.Source, store)
		if err != nil {
			return nil, err
		}
		h, ok := outputByName(outputs, input.Name)
		if !ok {
			return nil, fmt.Errorf("output %q not found in %s", input.Name, input.Source.Key())
		}
		resolved[input.Name] = h
	}
	return resolved, nil
}

// outputByName returns the hash of the output with the given name.
func outputByName(outputs []Output, name string) (objstore.Hash, bool) {
	for _, o := range outputs {
		if o.Name == name {
			return o.Hash, true
		}
	}
	return objstore.Hash{}, false
}
