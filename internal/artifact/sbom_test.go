package artifact

import (
	"io"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/objstore"
)

// readBlob returns a store blob's bytes as a string.
func readBlob(t *testing.T, store *objstore.Store, h objstore.Hash) string {
	t.Helper()
	r, err := store.Blobs.Open(h)
	if err != nil {
		t.Fatalf("open blob %s: %v", h, err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read blob %s: %v", h, err)
	}
	return string(b)
}

// buildOne builds a single-node graph for a and returns the store it built into.
func buildOne(t *testing.T, a Artifact) *objstore.Store {
	t.Helper()
	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewGraph()
	g.Add(a)
	results, err := NewEngine(g, store, 1).Run(testCtx())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("unexpected build result: %+v", results)
	}
	return store
}

// TestManifestCarriesProvenance checks that a built node's manifest lists the
// reserved .glbx-provenance output, and that the provenance blob records the
// node's identity and target key.
func TestManifestCarriesProvenance(t *testing.T) {
	a := &mockArtifact{name: "debian-pkg-build:fixture:amd64", identity: strings.Repeat("a", 64), sbom: []byte("{}\n")}
	store := buildOne(t, a)

	identity, _ := a.Identity()
	manifestHash, err := store.Map.Get(identity)
	if err != nil {
		t.Fatalf("manifest not stored: %v", err)
	}
	outputs, err := parseManifestBlob(store, manifestHash)
	if err != nil {
		t.Fatal(err)
	}

	var prov *Output
	for i := range outputs {
		if outputs[i].Name == ProvenanceOutputName {
			prov = &outputs[i]
		}
	}
	if prov == nil {
		t.Fatalf("manifest has no %s output; got %+v", ProvenanceOutputName, outputs)
	}

	body := readBlob(t, store, prov.Hash)
	if !strings.Contains(body, identity.String()) {
		t.Errorf("provenance missing identity: %s", body)
	}
	if !strings.Contains(body, a.Key()) {
		t.Errorf("provenance missing target key: %s", body)
	}
}

// TestSBOMStoredUnderMapKey checks that a built node writes its SBOM document as a
// blob reachable through the map key folded from the namespace and manifest hash.
func TestSBOMStoredUnderMapKey(t *testing.T) {
	const doc = "{\"dummy\":true}\n"
	a := &mockArtifact{name: "rootfs:base:amd64", identity: strings.Repeat("b", 64), sbom: []byte(doc)}
	store := buildOne(t, a)

	identity, _ := a.Identity()
	manifestHash, err := store.Map.Get(identity)
	if err != nil {
		t.Fatal(err)
	}

	key := sbomMapKey(SBOMNamespace(), manifestHash)
	sbomBlob, err := store.Map.Get(key)
	if err != nil {
		t.Fatalf("no sbom map entry at key %s: %v", key, err)
	}
	if got := readBlob(t, store, sbomBlob); got != doc {
		t.Errorf("sbom blob = %q, want %q", got, doc)
	}
}

// TestSBOMNamespaceSelectsKey checks that $GLBX_SBOM_NAMESPACE changes the SBOM
// map key, so the same manifest can carry SBOMs in separate namespaces.
func TestSBOMNamespaceSelectsKey(t *testing.T) {
	manifestHash := objstore.ConcatHash("some-manifest")
	def := sbomMapKey(defaultSBOMNamespace, manifestHash)

	t.Setenv("GLBX_SBOM_NAMESPACE", "gardenlinux.io")
	if SBOMNamespace() != "gardenlinux.io" {
		t.Fatalf("namespace override not honored: %s", SBOMNamespace())
	}
	ci := sbomMapKey(SBOMNamespace(), manifestHash)
	if ci == def {
		t.Fatalf("namespace did not change the key: %s", ci)
	}
}

// TestNoSBOMNoMapEntry checks that an artifact returning a nil SBOM writes no SBOM
// map entry, while still producing a provenance-bearing manifest.
func TestNoSBOMNoMapEntry(t *testing.T) {
	a := &mockArtifact{name: "image:disk:amd64", identity: strings.Repeat("c", 64), sbom: nil}
	store := buildOne(t, a)

	identity, _ := a.Identity()
	manifestHash, err := store.Map.Get(identity)
	if err != nil {
		t.Fatal(err)
	}
	key := sbomMapKey(SBOMNamespace(), manifestHash)
	if _, err := store.Map.Get(key); err == nil {
		t.Fatalf("expected no sbom map entry for nil-SBOM artifact, but found one at %s", key)
	}
}

// TestProvenanceReproducible checks that a freshly built node still verifies as
// reproducible: provenance is a deterministic function of (identity, target), so
// the rebuilt manifest hash equals the original.
func TestProvenanceReproducible(t *testing.T) {
	a := &mockArtifact{name: "debian-pkg-build:fixture:amd64", identity: strings.Repeat("d", 64), sbom: []byte("{}\n")}
	store := buildOne(t, a)

	g := NewGraph()
	g.Add(a)
	results, err := CheckReproducible(testCtx(), g, store, []string{a.Key()}, 1, ReproHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Err != nil {
		t.Fatalf("repro failed: %v", results[0].Err)
	}
	if !results[0].Match {
		t.Fatalf("expected reproducible match; original=%s reproduced=%s",
			results[0].OriginalManifest, results[0].ReproducedManifest)
	}
}
