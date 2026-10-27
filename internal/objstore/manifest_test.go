package objstore

import (
	"strings"
	"testing"
)

// TestSerializeManifestCanonicalOrder verifies the manifest is canonical: the
// same set of outputs serializes to identical bytes regardless of the order a
// producer emitted them in, with entries sorted by Name.
func TestSerializeManifestCanonicalOrder(t *testing.T) {
	a := HashBytes([]byte("a"))
	b := HashBytes([]byte("b"))
	c := HashBytes([]byte("c"))

	forward := SerializeManifest([]Output{
		{Name: "acl.deb", Hash: a},
		{Name: "control:acl", Hash: b},
		{Name: "libacl1.deb", Hash: c},
	})
	shuffled := SerializeManifest([]Output{
		{Name: "libacl1.deb", Hash: c},
		{Name: "acl.deb", Hash: a},
		{Name: "control:acl", Hash: b},
	})

	if forward != shuffled {
		t.Fatalf("manifest not order-independent:\nforward=\n%s\nshuffled=\n%s", forward, shuffled)
	}

	lines := strings.Split(strings.TrimRight(forward, "\n"), "\n")
	want := []string{
		a.String() + " acl.deb",
		b.String() + " control:acl",
		c.String() + " libacl1.deb",
	}
	if len(lines) != len(want) {
		t.Fatalf("expected %d lines, got %d: %q", len(want), len(lines), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}
