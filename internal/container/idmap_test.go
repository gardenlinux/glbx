package container

import "testing"

func TestIDMappingSingleRange(t *testing.T) {
	subRanges := []IDRange{{Start: 100000, Count: 65536}}
	parentMap := []IDMapping{{Inner: 0, Outer: 0, Count: 4294967295}}

	mappings, err := ComputeIDMappings(subRanges, parentMap, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(mappings))
	}
	if mappings[0].Inner != 0 || mappings[0].Outer != 100000 || mappings[0].Count != 65536 {
		t.Fatalf("unexpected mapping: %+v", mappings[0])
	}
}

func TestIDMappingInsufficientIDs(t *testing.T) {
	subRanges := []IDRange{{Start: 100000, Count: 100}}
	parentMap := []IDMapping{{Inner: 0, Outer: 0, Count: 4294967295}}

	if _, err := ComputeIDMappings(subRanges, parentMap, 65536); err == nil {
		t.Fatal("expected error for insufficient IDs")
	}
}

func TestIDMappingMultipleRanges(t *testing.T) {
	subRanges := []IDRange{
		{Start: 100000, Count: 30000},
		{Start: 200000, Count: 40000},
	}
	parentMap := []IDMapping{{Inner: 0, Outer: 0, Count: 4294967295}}

	mappings, err := ComputeIDMappings(subRanges, parentMap, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(mappings))
	}
	var total uint32
	for _, m := range mappings {
		total += m.Count
	}
	if total != 65536 {
		t.Fatalf("expected total 65536, got %d", total)
	}
}

// A subordinate range is clamped to the parent map's window: only the
// intersection is usable.
func TestIDMappingIntersectsParent(t *testing.T) {
	subRanges := []IDRange{{Start: 100000, Count: 65536}}
	// Parent only maps outer IDs [100000, 110000).
	parentMap := []IDMapping{{Inner: 0, Outer: 100000, Count: 10000}}

	mappings, err := ComputeIDMappings(subRanges, parentMap, 10000)
	if err != nil {
		t.Fatal(err)
	}
	var total uint32
	for _, m := range mappings {
		total += m.Count
		if m.Outer < 100000 || m.Outer+m.Count > 110000 {
			t.Errorf("mapping escapes parent window: %+v", m)
		}
	}
	if total != 10000 {
		t.Fatalf("expected total 10000, got %d", total)
	}

	// Asking for more than the intersection provides must fail.
	if _, err := ComputeIDMappings(subRanges, parentMap, 20000); err == nil {
		t.Fatal("expected error when request exceeds the usable intersection")
	}
}
