package artifact

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/gardenlinux/glbx/internal/objstore"
)

// SerializeManifest encodes outputs in the manifest format via
// objstore.SerializeManifest, the single source of truth for the byte format.
func SerializeManifest(outputs []Output) string {
	oo := make([]objstore.Output, len(outputs))
	for i, o := range outputs {
		oo[i] = objstore.Output{Name: o.Name, Hash: o.Hash}
	}
	return objstore.SerializeManifest(oo)
}

// storeManifest serializes outputs as "<hash> <name>\n" lines, stores as a blob,
// and maps identity → manifest blob hash. Returns the manifest blob hash so the
// caller can derive dependent keys (e.g. the SBOM map key) from it.
func (e *Engine) storeManifest(identity objstore.Hash, outputs []Output) (objstore.Hash, error) {
	manifest := SerializeManifest(outputs)
	manifestHash, err := e.store.Blobs.Store(strings.NewReader(manifest))
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("store manifest blob: %w", err)
	}
	if err := e.store.Map.Set(identity, manifestHash, false); err != nil {
		return objstore.Hash{}, err
	}
	return manifestHash, nil
}

// parseManifestBlob reads and parses a manifest blob into its outputs.
func parseManifestBlob(store *objstore.Store, manifestHash objstore.Hash) ([]Output, error) {
	reader, err := store.Blobs.Open(manifestHash)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var outputs []Output
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed manifest line: %q", line)
		}
		hash, err := objstore.NewHash(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid hash in manifest: %w", err)
		}
		outputs = append(outputs, Output{Name: parts[1], Hash: hash})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return outputs, nil
}

// ManifestBlobs returns the manifest blob hash followed by every leaf hash it
// references, i.e. the full set of blobs reachable from a manifest. It reads and
// parses the manifest blob directly, needing no artifact, identity, or graph.
func ManifestBlobs(store *objstore.Store, manifestHash objstore.Hash) ([]objstore.Hash, error) {
	outputs, err := parseManifestBlob(store, manifestHash)
	if err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", manifestHash, err)
	}
	blobs := make([]objstore.Hash, 0, len(outputs)+1)
	blobs = append(blobs, manifestHash)
	for _, o := range outputs {
		blobs = append(blobs, o.Hash)
	}
	return blobs, nil
}

// ResolveOutputs resolves a built artifact's identity → map → manifest blob and
// returns its named outputs. It errors if the artifact has no map entry (not
// built).
func ResolveOutputs(a Artifact, store *objstore.Store) ([]Output, error) {
	identity, err := a.Identity()
	if err != nil {
		return nil, fmt.Errorf("compute identity: %w", err)
	}
	manifestHash, err := store.Map.Get(identity)
	if err != nil {
		return nil, fmt.Errorf("%s not built: %w", a.Key(), err)
	}
	outputs, err := parseManifestBlob(store, manifestHash)
	if err != nil {
		return nil, fmt.Errorf("parse manifest for %s: %w", a.Key(), err)
	}
	return outputs, nil
}

// ResolveOutputRefs is the shared implementation of Artifact.OutputRefs. Given
// an artifact's identity, it resolves identity → map → manifest blob and parses
// the leaf references, returning the manifest blob hash and the leaf hashes.
// Returns an error if the artifact has no map entry (not built).
func ResolveOutputRefs(a Artifact, store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	identity, err := a.Identity()
	if err != nil {
		return objstore.Hash{}, nil, fmt.Errorf("compute identity: %w", err)
	}
	manifestHash, err := store.Map.Get(identity)
	if err != nil {
		return objstore.Hash{}, nil, fmt.Errorf("%s not built: %w", a.Key(), err)
	}
	outputs, err := parseManifestBlob(store, manifestHash)
	if err != nil {
		return objstore.Hash{}, nil, fmt.Errorf("parse manifest for %s: %w", a.Key(), err)
	}
	leaves := make([]objstore.Hash, 0, len(outputs))
	for _, o := range outputs {
		leaves = append(leaves, o.Hash)
	}
	return manifestHash, leaves, nil
}

// loadManifest reads and parses the manifest for an identity. It uses
// Map.Get, so on a local miss with a remote configured the manifest is
// reconstructed and its leaves pulled, turning a would-be rebuild into a
// cache hit; with no remote it is a plain local lookup.
func (e *Engine) loadManifest(identity objstore.Hash) ([]Output, error) {
	manifestHash, err := e.store.Map.Get(identity)
	if err != nil {
		return nil, err
	}
	return parseManifestBlob(e.store, manifestHash)
}
