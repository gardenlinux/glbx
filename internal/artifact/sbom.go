package artifact

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gardenlinux/glbx/internal/objstore"
)

// sbomSeed is the dedicated seed for the SBOM object-store map key. It is folded
// as the first element of every SBOM map key, so rotating it gives the same
// namespace and output manifest a fresh map key without touching artifact
// identities or the manifest itself. It is an arbitrary unguessable value, not a
// format requirement. Regenerate only when the SBOM *addressing* scheme changes
// (not when SBOM content changes, and never for an identity-scheme change):
//
//	openssl rand -hex 16
//
// Unlike the identity seeds, this one has no runtime override: the SBOM address
// is deliberately independent of $GLBX_ID_SEED_OVERRIDE so a separate identity
// namespace still resolves the same SBOM bytes.
const sbomSeed = "4a5de9ad2eed857df14d73b140931669"

// defaultSBOMNamespace is the SBOM namespace used when $GLBX_SBOM_NAMESPACE is
// unset. Local builds live under "localhost"; Garden Linux CI sets the env var to
// its own namespace (e.g. "gardenlinux.io").
const defaultSBOMNamespace = "localhost"

// ProvenanceOutputName is the reserved output every manifest carries. Its blob
// folds the build's target and identity into the manifest so that two builds with
// byte-identical payloads but different histories still get distinct manifest
// hashes. It is excluded from what shipped artifacts carry; see
// doc/src/concepts/sbom.md and doc/src/impl/engine.md.
const ProvenanceOutputName = ".glbx-provenance"

// SBOMNamespace returns the active SBOM namespace, honoring $GLBX_SBOM_NAMESPACE
// and defaulting to "localhost". It selects the map key under which an artifact's
// SBOM document is stored, so the same output manifest can carry a local SBOM and
// a CI SBOM side by side.
func SBOMNamespace() string {
	if ns := os.Getenv("GLBX_SBOM_NAMESPACE"); ns != "" {
		return ns
	}
	return defaultSBOMNamespace
}

// sbomMapKey folds the dedicated SBOM seed, the namespace, and the output-manifest
// hash into the object-store map key for an artifact's SBOM document. Using the
// same ConcatHash fold as every other key keeps the SBOM entry in the one map
// space without colliding with identity→manifest, log, or run entries, because the
// seed differs.
func sbomMapKey(namespace string, manifestHash objstore.Hash) objstore.Hash {
	return objstore.ConcatHash(sbomSeed, namespace, manifestHash.String())
}

// SBOMMapKey is the exported form of sbomMapKey, for callers (the CLI) that need
// to resolve or print an artifact's SBOM map key from a manifest hash.
func SBOMMapKey(namespace string, manifestHash objstore.Hash) objstore.Hash {
	return sbomMapKey(namespace, manifestHash)
}

// provenanceDoc is the deterministic JSON body of the .glbx-provenance blob. The
// field order is fixed by the struct so the serialized bytes — and therefore the
// blob hash and the manifest hash — are reproducible across builds.
type provenanceDoc struct {
	Identity string `json:"identity"`
	Target   string `json:"target"`
}

// provenanceOutput builds the reserved .glbx-provenance output for a node from its
// identity and target key, storing the deterministic JSON as a blob. The bytes are
// a pure function of (identity, targetKey), so an identical rebuild reproduces the
// same blob and hence the same manifest hash.
func provenanceOutput(identity objstore.Hash, targetKey string, store *objstore.Store) (Output, error) {
	b, err := json.Marshal(provenanceDoc{Identity: identity.String(), Target: targetKey})
	if err != nil {
		return Output{}, fmt.Errorf("marshal provenance: %w", err)
	}
	hash, err := store.Blobs.Store(strings.NewReader(string(b)))
	if err != nil {
		return Output{}, fmt.Errorf("store provenance blob: %w", err)
	}
	return Output{Name: ProvenanceOutputName, Hash: hash}, nil
}

// withProvenance returns outputs plus the reserved .glbx-provenance output. Both
// the normal build path and the reproducibility-check path call this before
// serializing the manifest, so the manifest hash they compute includes provenance
// identically and a reproducible rebuild still matches.
func withProvenance(outputs []Output, identity objstore.Hash, targetKey string, store *objstore.Store) ([]Output, error) {
	prov, err := provenanceOutput(identity, targetKey, store)
	if err != nil {
		return nil, err
	}
	out := make([]Output, 0, len(outputs)+1)
	out = append(out, outputs...)
	out = append(out, prov)
	return out, nil
}

// storeSBOM writes an artifact's SBOM document to the local store and records the
// map entry pointing at it. An artifact kind that produces no SBOM (SBOM returns
// nil) is a no-op. The map key is folded from the SBOM seed, the active namespace,
// and the artifact's output-manifest hash. Local store only — publication of the
// SBOM to a remote is a separate step not wired here.
func (e *Engine) storeSBOM(ctx context.Context, a Artifact, manifestHash objstore.Hash) error {
	doc, err := a.SBOM(ctx)
	if err != nil {
		return fmt.Errorf("generate sbom: %w", err)
	}
	if doc == nil {
		return nil
	}
	blobHash, err := e.store.Blobs.Store(strings.NewReader(string(doc)))
	if err != nil {
		return fmt.Errorf("store sbom blob: %w", err)
	}
	key := sbomMapKey(SBOMNamespace(), manifestHash)
	if err := e.store.Map.Set(key, blobHash, false); err != nil {
		return fmt.Errorf("record sbom map entry: %w", err)
	}
	return nil
}
