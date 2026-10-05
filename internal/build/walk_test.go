package build

import (
	"testing"
)

// makeBinaryPkg returns a fresh debianBinaryPkg attached to a fresh source
// build, with depsResolved=true so walkBinaries does not try to load a
// build.yml that does not exist.
func makeBinaryPkg(srcName, binName string) *debianBinaryPkg {
	sb := &DebianPkgBuild{Name: srcName, Arch: "amd64"}
	return &debianBinaryPkg{
		name:         binName,
		sourceBuild:  sb,
		depsResolved: true,
	}
}

func names(bps []*debianBinaryPkg) []string {
	out := make([]string, len(bps))
	for i, bp := range bps {
		out[i] = bp.sourceBuild.Name + ":" + bp.name
	}
	return out
}

// TestWalkBinaries_ExtraDeps verifies DFS over extraDeps with dedupe.
func TestWalkBinaries_ExtraDeps(t *testing.T) {
	a := makeBinaryPkg("src-a", "a")
	b := makeBinaryPkg("src-b", "b")
	c := makeBinaryPkg("src-c", "c")

	// a → b → c, plus a → c (c reachable two ways).
	a.extraDeps = []*debianBinaryPkg{b, c}
	b.extraDeps = []*debianBinaryPkg{c}

	got := walkBinaries([]*debianBinaryPkg{a})
	if len(got) != 3 {
		t.Fatalf("expected 3 unique binaries, got %d: %v", len(got), names(got))
	}
	// First-seen DFS: a, then b (a's first extra), c reached via b before a's c edge.
	if got[0] != a || got[1] != b || got[2] != c {
		t.Errorf("unexpected order: %v", names(got))
	}
}

// TestWalkBinaries_Includes verifies the sibling-includes branch is walked.
func TestWalkBinaries_Includes(t *testing.T) {
	a := makeBinaryPkg("src", "a")
	b := makeBinaryPkg("src", "b")
	a.includes = []*debianBinaryPkg{b}

	got := walkBinaries([]*debianBinaryPkg{a})
	if len(got) != 2 {
		t.Fatalf("expected 2 binaries, got %d: %v", len(got), names(got))
	}
	if got[1] != b {
		t.Errorf("expected sibling b to be reached via includes; got %v", names(got))
	}
}

// TestWalkBinaries_MutualIncludesCycle verifies the libssl3t64 ↔ openssl-provider-legacy
// shape: two siblings whose runtime_depends point at each other do not infinite-loop.
func TestWalkBinaries_MutualIncludesCycle(t *testing.T) {
	a := makeBinaryPkg("src", "a")
	b := makeBinaryPkg("src", "b")
	a.includes = []*debianBinaryPkg{b}
	b.includes = []*debianBinaryPkg{a}

	got := walkBinaries([]*debianBinaryPkg{a})
	if len(got) != 2 {
		t.Fatalf("expected 2 binaries (no infinite loop), got %d: %v", len(got), names(got))
	}
}

// TestWalkBinaries_ExtraDepsCycle verifies a cross-source cycle terminates.
func TestWalkBinaries_ExtraDepsCycle(t *testing.T) {
	a := makeBinaryPkg("src-a", "a")
	b := makeBinaryPkg("src-b", "b")
	a.extraDeps = []*debianBinaryPkg{b}
	b.extraDeps = []*debianBinaryPkg{a}

	got := walkBinaries([]*debianBinaryPkg{a})
	if len(got) != 2 {
		t.Fatalf("expected 2 binaries (no infinite loop), got %d", len(got))
	}
}

// TestWalkBinaries_KeyDistinguishesBySource confirms that two binaries sharing
// a name but belonging to different source builds are NOT collapsed by the
// seen set — the dedupe key is "<source>:<binary>", not just "<binary>".
func TestWalkBinaries_KeyDistinguishesBySource(t *testing.T) {
	a := makeBinaryPkg("src-a", "shared-name")
	b := makeBinaryPkg("src-b", "shared-name")
	root := makeBinaryPkg("src-root", "root")
	root.extraDeps = []*debianBinaryPkg{a, b}

	got := walkBinaries([]*debianBinaryPkg{root})
	if len(got) != 3 {
		t.Fatalf("expected 3 distinct binaries (different sources), got %d: %v", len(got), names(got))
	}
}

// TestWalkBinaries_MultipleRoots verifies that passing multiple roots produces
// a single union closure with dedupe across roots.
func TestWalkBinaries_MultipleRoots(t *testing.T) {
	a := makeBinaryPkg("src-a", "a")
	b := makeBinaryPkg("src-b", "b")
	shared := makeBinaryPkg("src-shared", "shared")
	a.extraDeps = []*debianBinaryPkg{shared}
	b.extraDeps = []*debianBinaryPkg{shared}

	got := walkBinaries([]*debianBinaryPkg{a, b})
	if len(got) != 3 {
		t.Fatalf("expected 3 distinct binaries (shared dedup'd across roots), got %d: %v",
			len(got), names(got))
	}
}
