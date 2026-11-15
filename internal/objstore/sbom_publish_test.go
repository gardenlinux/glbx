package objstore

import (
	"strings"
	"testing"
)

// TestEnsureSBOMClosure checks that a sbom-<M> closure manifest is pushed with
// the right artifact type, one spdx+json layer per document in order, and title
// annotations, and that a second call never overwrites it (first writer wins).
func TestEnsureSBOMClosure(t *testing.T) {
	reg, _ := newTestRegistry(t)
	local := newLocalForTest(t)

	// Three SPDX doc blobs standing in for a root + two producers.
	root := MustHash(strings.Repeat("a", 64))
	p1 := MustHash(strings.Repeat("b", 64))
	p2 := MustHash(strings.Repeat("c", 64))
	dRoot, _ := local.Store(strings.NewReader("root spdx"))
	dP1, _ := local.Store(strings.NewReader("p1 spdx"))
	dP2, _ := local.Store(strings.NewReader("p2 spdx"))

	pub, err := NewPublisher("http://"+regRef(reg), false, local)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	// The document blobs must exist on the registry before the closure manifest
	// references them (the ordinary worklist does this in a real publish).
	for _, h := range []Hash{dRoot, dP1, dP2} {
		if _, _, err := pub.EnsureBlob(h); err != nil {
			t.Fatalf("EnsureBlob %s: %v", h, err)
		}
	}

	layers := []SBOMClosureLayer{
		{Doc: dRoot, Size: 9, Manifest: root},
		{Doc: dP1, Size: 7, Manifest: p1},
		{Doc: dP2, Size: 7, Manifest: p2},
	}
	set, err := pub.EnsureSBOMClosure(root, layers)
	if err != nil {
		t.Fatalf("EnsureSBOMClosure: %v", err)
	}
	if !set {
		t.Fatal("first EnsureSBOMClosure should report set=true")
	}

	// Read the pushed manifest back and check its shape.
	m, err := reg.getManifest(sbomClosureTag(root))
	if err != nil {
		t.Fatalf("getManifest sbom tag: %v", err)
	}
	if m.ArtifactType != sbomClosureArtifactType {
		t.Errorf("artifactType = %q, want %q", m.ArtifactType, sbomClosureArtifactType)
	}
	if len(m.Layers) != 3 {
		t.Fatalf("layers = %d, want 3", len(m.Layers))
	}
	wantTitles := []string{root.String() + ".spdx.json", p1.String() + ".spdx.json", p2.String() + ".spdx.json"}
	wantDigests := []string{"sha256:" + dRoot.String(), "sha256:" + dP1.String(), "sha256:" + dP2.String()}
	for i, l := range m.Layers {
		if l.MediaType != spdxMediaType {
			t.Errorf("layer %d mediaType = %q, want %q", i, l.MediaType, spdxMediaType)
		}
		if l.Digest != wantDigests[i] {
			t.Errorf("layer %d digest = %q, want %q", i, l.Digest, wantDigests[i])
		}
		if got := l.Annotations[ociTitleAnnotation]; got != wantTitles[i] {
			t.Errorf("layer %d title = %q, want %q", i, got, wantTitles[i])
		}
	}

	// Never overwrite: a second call is a no-op.
	set, err = pub.EnsureSBOMClosure(root, layers)
	if err != nil {
		t.Fatalf("second EnsureSBOMClosure: %v", err)
	}
	if set {
		t.Error("second EnsureSBOMClosure should report set=false (already present)")
	}
}
