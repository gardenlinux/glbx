package artifact

import (
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/objstore"
)

// storeSBOMDoc writes a serialized SPDX document blob and its K(A,M)→D map entry,
// returning the document blob hash D.
func storeSBOMDoc(t *testing.T, store *objstore.Store, ns string, m objstore.Hash, body string) objstore.Hash {
	t.Helper()
	d, err := store.Blobs.Store(strings.NewReader(body))
	if err != nil {
		t.Fatalf("store sbom doc: %v", err)
	}
	if err := store.Map.Set(SBOMMapKey(ns, m), d, false); err != nil {
		t.Fatalf("set sbom map: %v", err)
	}
	return d
}

// TestSBOMClosure resolves a two-document closure: a validation document that
// imports a source document. The closure must be root-first, then the producer.
func TestSBOMClosure(t *testing.T) {
	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ns := "localhost"
	srcM := objstore.ConcatHash("source-manifest")
	valM := objstore.ConcatHash("validation-manifest")

	// Source document: no imports.
	srcBody := `{"@graph":[{"type":"SpdxDocument","spdxId":"urn:spdx:localhost/glbx/sboms/` + srcM.String() + `/document"}]}`
	srcD := storeSBOMDoc(t, store, ns, srcM, srcBody)

	// Validation document: imports the source's output element.
	valBody := `{"@graph":[{"type":"SpdxDocument","spdxId":"urn:spdx:localhost/glbx/sboms/` + valM.String() + `/document","import":[{"type":"ExternalMap","externalSpdxId":"urn:spdx:localhost/glbx/sboms/` + srcM.String() + `/outputs/fixture.deb"}]}]}`
	valD := storeSBOMDoc(t, store, ns, valM, valBody)

	refs, found, err := SBOMClosure(store, ns, valM)
	if err != nil {
		t.Fatalf("SBOMClosure: %v", err)
	}
	if !found {
		t.Fatal("expected closure found")
	}
	if len(refs) != 2 {
		t.Fatalf("closure size = %d, want 2", len(refs))
	}
	if refs[0].Manifest != valM || refs[0].Doc != valD {
		t.Errorf("root ref = %+v, want manifest=%s doc=%s", refs[0], valM, valD)
	}
	if refs[1].Manifest != srcM || refs[1].Doc != srcD {
		t.Errorf("producer ref = %+v, want manifest=%s doc=%s", refs[1], srcM, srcD)
	}
}

// TestSBOMClosureMissing reports found=false (no error) when the root SBOM is
// absent from the local store.
func TestSBOMClosureMissing(t *testing.T) {
	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := SBOMClosure(store, "localhost", objstore.ConcatHash("absent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for absent SBOM")
	}
}

// TestSBOMClosureMissingProducer errors when a referenced producer SBOM is
// absent — an incomplete closure must not be silently published.
func TestSBOMClosureMissingProducer(t *testing.T) {
	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ns := "localhost"
	valM := objstore.ConcatHash("validation-manifest")
	missingM := objstore.ConcatHash("missing-producer")
	valBody := `{"@graph":[{"type":"SpdxDocument","import":[{"type":"ExternalMap","externalSpdxId":"urn:spdx:localhost/glbx/sboms/` + missingM.String() + `/outputs/x.deb"}]}]}`
	storeSBOMDoc(t, store, ns, valM, valBody)

	if _, _, err := SBOMClosure(store, ns, valM); err == nil {
		t.Fatal("expected error for missing producer in closure")
	}
}
