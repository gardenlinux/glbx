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

// depKeys returns the Key() of every Depends() edge of bp.
func depKeys(bp *debianBinaryPkg) map[string]bool {
	out := map[string]bool{}
	for _, d := range bp.Depends() {
		out[d.Key()] = true
	}
	return out
}

// TestDepends_IncludeCrossSourceDeps is the regression guard for the install-check
// ordering bug. Shape mirrors pam: libpam-runtime Includes libpam-modules (same
// source "pam"); libpam-modules Depends glibc:libc6 (cross-source). The install
// check for libpam-runtime resolves libpam-modules' closure, so it must be
// build-ordered after libc6 — but NOT after libpam-modules itself (same source,
// co-produced, ordering it would be a cycle). Depends() must therefore surface
// libc6 (via the sibling include) while never emitting an edge to the sibling.
func TestDepends_IncludeCrossSourceDeps(t *testing.T) {
	runtime := makeBinaryPkg("pam", "libpam-runtime")
	modules := makeBinaryPkg("pam", "libpam-modules")
	libc6 := makeBinaryPkg("glibc", "libc6")

	// runtime Includes modules (sibling); modules Depends libc6 (cross-source).
	runtime.includes = []*debianBinaryPkg{modules}
	modules.extraDeps = []*debianBinaryPkg{libc6}

	deps := depKeys(runtime)

	// Must be ordered after libc6 (reached through the sibling include).
	if !deps["debian-binary-pkg:glibc:libc6:amd64"] {
		t.Errorf("libpam-runtime must Depend on libc6 (cross-source dep of its sibling include); got %v", deps)
	}
	// Must be ordered after its own source build (always).
	if !deps["debian-pkg-build:pam:amd64"] {
		t.Errorf("libpam-runtime must Depend on its source build; got %v", deps)
	}
	// Must NOT be ordered after the sibling binary itself (same source → cycle).
	if deps["debian-binary-pkg:pam:libpam-modules:amd64"] {
		t.Errorf("libpam-runtime must NOT Depend on its same-source sibling libpam-modules; got %v", deps)
	}
}

// TestDepends_NoIncludesUnchanged verifies the fix is inert for a binary with no
// sibling includes: Depends() is exactly its source build plus its own extraDeps,
// with no transitive flattening of those extraDeps' own dependencies.
func TestDepends_NoIncludesUnchanged(t *testing.T) {
	libacl1 := makeBinaryPkg("acl", "libacl1")
	libc6 := makeBinaryPkg("glibc", "libc6")
	gccBase := makeBinaryPkg("gcc-16", "gcc-16-base")

	// libacl1 Depends libc6 directly; libc6 Depends gcc-16-base (one hop further).
	libacl1.extraDeps = []*debianBinaryPkg{libc6}
	libc6.extraDeps = []*debianBinaryPkg{gccBase}

	deps := depKeys(libacl1)

	if !deps["debian-binary-pkg:glibc:libc6:amd64"] {
		t.Errorf("libacl1 must Depend on its direct extraDep libc6; got %v", deps)
	}
	if !deps["debian-pkg-build:acl:amd64"] {
		t.Errorf("libacl1 must Depend on its source build; got %v", deps)
	}
	// No includes → no transitive flattening: gcc-16-base is libc6's dep, reached
	// transitively by the engine, and must NOT become a direct edge of libacl1.
	if deps["debian-binary-pkg:gcc-16:gcc-16-base:amd64"] {
		t.Errorf("libacl1 (no includes) must NOT gain a transitive edge to gcc-16-base; got %v", deps)
	}
}

// TestIncludeExtraDeps_TransitiveSiblings verifies the sibling walk recurses
// through chained same-source includes to collect their cross-source deps, and
// excludes the siblings themselves.
func TestIncludeExtraDeps_TransitiveSiblings(t *testing.T) {
	// a Includes b (same source); b Includes c (same source); c Depends x (cross).
	a := makeBinaryPkg("s", "a")
	b := makeBinaryPkg("s", "b")
	c := makeBinaryPkg("s", "c")
	x := makeBinaryPkg("other", "x")
	a.includes = []*debianBinaryPkg{b}
	b.includes = []*debianBinaryPkg{c}
	c.extraDeps = []*debianBinaryPkg{x}

	got := map[string]bool{}
	for _, d := range a.includeExtraDeps() {
		got[d.sourceBuild.Name+":"+d.name] = true
	}
	if !got["other:x"] {
		t.Errorf("expected cross-source x reached through chained sibling includes; got %v", got)
	}
	// Siblings themselves are never returned (same source, no ordering).
	if got["s:b"] || got["s:c"] {
		t.Errorf("sibling binaries must not appear in includeExtraDeps; got %v", got)
	}
}

// TestDepends_SharedSiblingFanOut verifies the fan-out shape: two siblings Y and
// Z both Include the same sibling X, and X Depends a cross-source B. Both Y and Z
// must be ordered after B (through the shared include X), and neither after X
// itself.
func TestDepends_SharedSiblingFanOut(t *testing.T) {
	x := makeBinaryPkg("A", "X")
	y := makeBinaryPkg("A", "Y")
	z := makeBinaryPkg("A", "Z")
	b := makeBinaryPkg("B", "B")

	x.extraDeps = []*debianBinaryPkg{b} // A:X Depends cross-source B:B
	y.includes = []*debianBinaryPkg{x}  // A:Y Includes sibling A:X
	z.includes = []*debianBinaryPkg{x}  // A:Z Includes sibling A:X

	for _, bp := range []*debianBinaryPkg{y, z} {
		deps := depKeys(bp)
		if !deps["debian-binary-pkg:B:B:amd64"] {
			t.Errorf("A:%s must Depend on B:B via shared sibling X; got %v", bp.name, deps)
		}
		if deps["debian-binary-pkg:A:X:amd64"] {
			t.Errorf("A:%s must NOT Depend on its same-source sibling X; got %v", bp.name, deps)
		}
	}
}
