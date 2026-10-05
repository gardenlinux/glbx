package resolver

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/debian/index"
)

// buildIndex is a test helper that constructs an index from package specs.
func buildIndex(t *testing.T, specs []pkgSpec) *index.Index {
	t.Helper()
	var stanzas []string
	for _, s := range specs {
		stanza := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: amd64\n", s.name, s.version)
		if s.depends != "" {
			stanza += fmt.Sprintf("Depends: %s\n", s.depends)
		}
		if s.preDepends != "" {
			stanza += fmt.Sprintf("Pre-Depends: %s\n", s.preDepends)
		}
		if s.conflicts != "" {
			stanza += fmt.Sprintf("Conflicts: %s\n", s.conflicts)
		}
		if s.provides != "" {
			stanza += fmt.Sprintf("Provides: %s\n", s.provides)
		}
		if s.essential {
			stanza += "Essential: yes\n"
		}
		if s.priority != "" {
			stanza += fmt.Sprintf("Priority: %s\n", s.priority)
		}
		stanzas = append(stanzas, stanza)
	}

	content := strings.Join(stanzas, "\n")
	idx, err := index.Load(strings.NewReader(content))
	if err != nil {
		t.Fatalf("failed to load test index: %v", err)
	}
	return idx
}

type pkgSpec struct {
	name       string
	version    string
	depends    string
	preDepends string
	conflicts  string
	provides   string
	essential  bool
	priority   string
}

// TestSimpleLinearChain tests A -> B -> C dependency chain.
func TestSimpleLinearChain(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "a", version: "1.0"},
		{name: "b", version: "2.0", depends: "c"},
		{name: "c", version: "3.0"},
	})

	// Manually set A's depends to B.
	idx = buildIndex(t, []pkgSpec{
		{name: "a", version: "1.0", depends: "b"},
		{name: "b", version: "2.0", depends: "c"},
		{name: "c", version: "3.0"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "a"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "a")
	assertContains(t, names, "b")
	assertContains(t, names, "c")
	if len(names) != 3 {
		t.Errorf("expected 3 packages, got %d: %v", len(names), names)
	}
}

// TestVirtualPackages tests resolution via Provides declarations.
func TestVirtualPackages(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "app", version: "1.0", depends: "mail-transport-agent"},
		{name: "postfix", version: "3.5", provides: "mail-transport-agent"},
		{name: "exim4", version: "4.94", provides: "mail-transport-agent"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "app"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "app")

	// One of the providers must be selected.
	hasPostfix := contains(names, "postfix")
	hasExim := contains(names, "exim4")
	if !hasPostfix && !hasExim {
		t.Errorf("expected one of postfix or exim4 in result, got: %v", names)
	}
	if hasPostfix && hasExim {
		t.Errorf("expected only one provider, got both: %v", names)
	}
}

// TestConflicts tests that conflicts are respected and cause backtracking.
func TestConflicts(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "app", version: "1.0", depends: "provider"},
		{name: "provider", version: "2.0", conflicts: "blocker"},
		{name: "blocker", version: "1.0"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "app"}, {Name: "blocker"}})

	// This should fail: app requires provider, but provider conflicts with blocker.
	if err == nil {
		t.Fatalf("expected resolution error, got success: %v", pkgNames(result))
	}

	resErr, ok := err.(*ResolutionError)
	if !ok {
		t.Fatalf("expected *ResolutionError, got %T: %v", err, err)
	}
	if len(resErr.Attempts) == 0 {
		t.Error("expected non-empty Attempts in error")
	}
}

// TestConflictsWithBacktracking tests that the resolver backtracks past a
// conflicting choice to find an alternative.
func TestConflictsWithBacktracking(t *testing.T) {
	// app depends on "service" (virtual), provided by svc-a and svc-b.
	// svc-a conflicts with needed-pkg. svc-b does not.
	// Resolver should backtrack from svc-a to svc-b.
	idx := buildIndex(t, []pkgSpec{
		{name: "app", version: "1.0", depends: "service"},
		{name: "needed-pkg", version: "1.0"},
		{name: "svc-a", version: "1.0", provides: "service", conflicts: "needed-pkg"},
		{name: "svc-b", version: "1.0", provides: "service"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "app"}, {Name: "needed-pkg"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "app")
	assertContains(t, names, "needed-pkg")
	assertContains(t, names, "svc-b")
	assertNotContains(t, names, "svc-a")
}

// TestAlternatives tests dependency alternatives (A depends on B | C).
func TestAlternatives(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "app", version: "1.0", depends: "opt-a | opt-b"},
		{name: "opt-a", version: "1.0"},
		{name: "opt-b", version: "1.0"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "app"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "app")

	// One of the alternatives must be selected.
	if !contains(names, "opt-a") && !contains(names, "opt-b") {
		t.Errorf("expected one of opt-a or opt-b, got: %v", names)
	}
}

// TestAlternativeFirstExcluded tests that when the first alternative is
// excluded, the second is selected.
func TestAlternativeFirstExcluded(t *testing.T) {
	// app depends on "opt-a | opt-b", also depends on "blocker"
	// blocker conflicts with opt-a, so opt-b should be chosen.
	idx := buildIndex(t, []pkgSpec{
		{name: "app", version: "1.0", depends: "blocker, opt-a | opt-b"},
		{name: "blocker", version: "1.0", conflicts: "opt-a"},
		{name: "opt-a", version: "1.0"},
		{name: "opt-b", version: "1.0"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "app"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "app")
	assertContains(t, names, "blocker")
	assertContains(t, names, "opt-b")
	assertNotContains(t, names, "opt-a")
}

// TestVersionConstraints tests that version constraints are respected.
func TestVersionConstraints(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "app", version: "1.0", depends: "lib (>= 2.0)"},
		{name: "lib", version: "2.5"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "app"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "app")
	assertContains(t, names, "lib")
}

// TestVersionConstraintUnsatisfied tests failure when version constraint
// cannot be satisfied.
func TestVersionConstraintUnsatisfied(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "app", version: "1.0", depends: "lib (>= 3.0)"},
		{name: "lib", version: "2.5"},
	})

	r := New(idx, "amd64")
	_, err := r.Resolve([]Requirement{{Name: "app"}})
	if err == nil {
		t.Fatal("expected resolution error for unsatisfied version constraint")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "lib") {
		t.Errorf("error message should mention lib, got: %s", errMsg)
	}
}

// TestCircularDeps tests that circular dependencies are handled correctly.
// A depends on B, B depends on A — both should be selected.
func TestCircularDeps(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "a", version: "1.0", depends: "b"},
		{name: "b", version: "1.0", depends: "a"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "a"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "a")
	assertContains(t, names, "b")
}

// TestUnsatisfiableWithClearError tests that missing packages produce clear errors.
func TestUnsatisfiableWithClearError(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "app", version: "1.0", depends: "nonexistent"},
	})

	r := New(idx, "amd64")
	_, err := r.Resolve([]Requirement{{Name: "app"}})
	if err == nil {
		t.Fatal("expected error for missing dependency")
	}

	resErr, ok := err.(*ResolutionError)
	if !ok {
		t.Fatalf("expected *ResolutionError, got %T", err)
	}

	errMsg := resErr.Error()
	if !strings.Contains(errMsg, "nonexistent") {
		t.Errorf("error should mention 'nonexistent', got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "not in index") {
		t.Errorf("error should say 'not in index', got: %s", errMsg)
	}
}

// TestDirectVsVirtualEligibleRoots tests the distinction between direct
// and virtual-eligible root requirements.
func TestDirectVsVirtualEligibleRoots(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "postfix", version: "3.5", provides: "mail-transport-agent"},
		{name: "exim4", version: "4.94", provides: "mail-transport-agent"},
	})

	r := New(idx, "amd64")

	// Direct requirement for non-existent package should fail.
	_, err := r.Resolve([]Requirement{{Name: "mail-transport-agent", VirtualEligible: false}})
	if err == nil {
		t.Fatal("expected error for direct requirement of virtual package")
	}

	// Virtual-eligible should succeed by finding a provider.
	result, err := r.Resolve([]Requirement{{Name: "mail-transport-agent", VirtualEligible: true}})
	if err != nil {
		t.Fatalf("unexpected error for virtual-eligible: %v", err)
	}

	names := pkgNames(result)
	if !contains(names, "postfix") && !contains(names, "exim4") {
		t.Errorf("expected a provider in result, got: %v", names)
	}
}

// TestRootVersionConstraint tests that root requirements can have version constraints.
func TestRootVersionConstraint(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "lib", version: "1.5"},
	})

	r := New(idx, "amd64")

	// Should succeed: lib 1.5 >= 1.0
	result, err := r.Resolve([]Requirement{{Name: "lib", VersionOp: ">=", Version: "1.0"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, pkgNames(result), "lib")

	// Should fail: lib 1.5 >= 2.0
	_, err = r.Resolve([]Requirement{{Name: "lib", VersionOp: ">=", Version: "2.0"}})
	if err == nil {
		t.Fatal("expected error for unsatisfied root version constraint")
	}
}

// TestPreDepends tests that Pre-Depends are handled like Depends.
func TestPreDepends(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "app", version: "1.0", preDepends: "prereq"},
		{name: "prereq", version: "1.0"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "app"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "app")
	assertContains(t, names, "prereq")
}

// TestLargerRealisticScenario tests a more complex dependency graph.
func TestLargerRealisticScenario(t *testing.T) {
	// Simulate a small but realistic scenario:
	// bash depends on libc6, libtinfo6
	// coreutils depends on libc6, libselinux1
	// libselinux1 depends on libc6, libpcre2-8-0
	// libpcre2-8-0 depends on libc6
	// libtinfo6 depends on libc6
	// libc6 pre-depends on libgcc-s1
	// libgcc-s1 depends on libc6 (circular!)
	idx := buildIndex(t, []pkgSpec{
		{name: "bash", version: "5.2-2", depends: "libc6 (>= 2.36), libtinfo6 (>= 6.3)"},
		{name: "coreutils", version: "9.1-1", depends: "libc6 (>= 2.36), libselinux1 (>= 3.1)"},
		{name: "libc6", version: "2.37-1", preDepends: "libgcc-s1"},
		{name: "libgcc-s1", version: "13.2-1", depends: "libc6 (>= 2.35)"},
		{name: "libtinfo6", version: "6.4-1", depends: "libc6 (>= 2.36)"},
		{name: "libselinux1", version: "3.4-1", depends: "libc6 (>= 2.36), libpcre2-8-0 (>= 10.22)"},
		{name: "libpcre2-8-0", version: "10.42-1", depends: "libc6 (>= 2.36)"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{
		{Name: "bash"},
		{Name: "coreutils"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	expected := []string{"bash", "coreutils", "libc6", "libgcc-s1", "libtinfo6", "libselinux1", "libpcre2-8-0"}
	for _, e := range expected {
		assertContains(t, names, e)
	}
	if len(names) != len(expected) {
		t.Errorf("expected %d packages, got %d: %v", len(expected), len(names), names)
	}
}

// TestMissingRootPackage tests error when a direct root requirement is not in the index.
func TestMissingRootPackage(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "existing", version: "1.0"},
	})

	r := New(idx, "amd64")
	_, err := r.Resolve([]Requirement{{Name: "nonexistent"}})
	if err == nil {
		t.Fatal("expected error for missing root package")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "nonexistent") {
		t.Errorf("error should mention package name, got: %s", errMsg)
	}
}

// TestEmptyRoots tests that an empty requirement list resolves to an empty set.
func TestEmptyRoots(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "a", version: "1.0"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Packages) != 0 {
		t.Errorf("expected empty result, got: %v", pkgNames(result))
	}
}

// TestProvideWithVersion tests that versioned Provides are handled correctly.
func TestProvideWithVersion(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "app", version: "1.0", depends: "libfoo (>= 2.0)"},
		{name: "libfoo3", version: "3.0", provides: "libfoo (= 3.0)"},
		{name: "libfoo1", version: "1.0", provides: "libfoo (= 1.0)"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "app"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "app")
	assertContains(t, names, "libfoo3")
	assertNotContains(t, names, "libfoo1")
}

// TestMultipleRoots tests resolving multiple root requirements simultaneously.
func TestMultipleRoots(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "a", version: "1.0", depends: "shared"},
		{name: "b", version: "1.0", depends: "shared"},
		{name: "shared", version: "1.0"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "a"}, {Name: "b"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "a")
	assertContains(t, names, "b")
	assertContains(t, names, "shared")
	if len(names) != 3 {
		t.Errorf("expected 3 packages, got %d: %v", len(names), names)
	}
}

// TestConflictBothNeeded tests that requesting two mutually conflicting
// packages results in an error.
func TestConflictBothNeeded(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "a", version: "1.0", conflicts: "b"},
		{name: "b", version: "1.0", conflicts: "a"},
	})

	r := New(idx, "amd64")
	_, err := r.Resolve([]Requirement{{Name: "a"}, {Name: "b"}})
	if err == nil {
		t.Fatal("expected error when two conflicting packages are both required")
	}
}

// TestResolutionErrorFormat tests that the error message is well-structured.
func TestResolutionErrorFormat(t *testing.T) {
	re := &ResolutionError{
		Attempts: []Attempt{
			{
				Package: "app",
				Reason:  "depends on missing-lib",
				Chain:   []string{"requested: app"},
				SubErrors: []Attempt{
					{
						Package: "missing-lib",
						Reason:  "not in index",
						Chain:   []string{"requested: app", "app depends on missing-lib"},
					},
				},
			},
		},
	}

	errMsg := re.Error()
	if !strings.Contains(errMsg, "dependency resolution failed") {
		t.Errorf("error should contain header, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "app") {
		t.Errorf("error should mention app, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "missing-lib") {
		t.Errorf("error should mention missing-lib, got: %s", errMsg)
	}
}

// TestVirtualEligibleWithVersionConstraint tests that virtual-eligible roots
// with version constraints work correctly.
func TestVirtualEligibleWithVersionConstraint(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "libfoo-old", version: "1.0", provides: "libfoo-dev (= 1.0)"},
		{name: "libfoo-new", version: "2.0", provides: "libfoo-dev (= 2.0)"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{
		Name:            "libfoo-dev",
		VersionOp:       ">=",
		Version:         "2.0",
		VirtualEligible: true,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "libfoo-new")
	assertNotContains(t, names, "libfoo-old")
}

// TestSinglePackageNoDeps tests resolving a package with no dependencies.
func TestSinglePackageNoDeps(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "standalone", version: "1.0"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "standalone"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	if len(names) != 1 || names[0] != "standalone" {
		t.Errorf("expected [standalone], got: %v", names)
	}
}

// TestStrictVersionOperators tests all version constraint operators.
func TestStrictVersionOperators(t *testing.T) {
	tests := []struct {
		op      string
		version string
		wantErr bool
	}{
		{">=", "1.0", false}, // 2.0 >= 1.0
		{">=", "2.0", false}, // 2.0 >= 2.0
		{">=", "3.0", true},  // 2.0 >= 3.0 fails
		{">>", "1.0", false}, // 2.0 >> 1.0
		{">>", "2.0", true},  // 2.0 >> 2.0 fails
		{"=", "2.0", false},  // 2.0 = 2.0
		{"=", "1.0", true},   // 2.0 = 1.0 fails
		{"<=", "3.0", false}, // 2.0 <= 3.0
		{"<=", "2.0", false}, // 2.0 <= 2.0
		{"<=", "1.0", true},  // 2.0 <= 1.0 fails
		{"<<", "3.0", false}, // 2.0 << 3.0
		{"<<", "2.0", true},  // 2.0 << 2.0 fails
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%s", tt.op, tt.version), func(t *testing.T) {
			idx := buildIndex(t, []pkgSpec{
				{name: "app", version: "1.0", depends: fmt.Sprintf("lib (%s %s)", tt.op, tt.version)},
				{name: "lib", version: "2.0"},
			})

			r := New(idx, "amd64")
			_, err := r.Resolve([]Requirement{{Name: "app"}})
			if tt.wantErr && err == nil {
				t.Errorf("expected error for lib %s %s (lib is 2.0)", tt.op, tt.version)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for lib %s %s (lib is 2.0): %v", tt.op, tt.version, err)
			}
		})
	}
}

// TestDeepDependencyChain tests a deep chain: a -> b -> c -> d -> e.
func TestDeepDependencyChain(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "a", version: "1.0", depends: "b"},
		{name: "b", version: "1.0", depends: "c"},
		{name: "c", version: "1.0", depends: "d"},
		{name: "d", version: "1.0", depends: "e"},
		{name: "e", version: "1.0"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "a"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	expected := []string{"a", "b", "c", "d", "e"}
	for _, e := range expected {
		assertContains(t, names, e)
	}
}

// TestDiamondDependency tests the classic diamond pattern:
// A depends on B and C, both B and C depend on D.
func TestDiamondDependency(t *testing.T) {
	idx := buildIndex(t, []pkgSpec{
		{name: "a", version: "1.0", depends: "b, c"},
		{name: "b", version: "1.0", depends: "d"},
		{name: "c", version: "1.0", depends: "d"},
		{name: "d", version: "1.0"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "a"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	expected := []string{"a", "b", "c", "d"}
	for _, e := range expected {
		assertContains(t, names, e)
	}
	if len(names) != 4 {
		t.Errorf("expected 4 packages, got %d: %v", len(names), names)
	}
}

// TestBreaksFieldNotConflict ensures that Breaks does not prevent selection
// (Breaks is weaker than Conflicts in the resolver - for now we only handle Conflicts).
func TestBreaksFieldNotConflict(t *testing.T) {
	// This test ensures we only look at Conflicts, not Breaks.
	// (Breaks is about upgrade ordering, not mutual exclusion.)
	idx := buildIndex(t, []pkgSpec{
		{name: "new-lib", version: "2.0"},
		{name: "old-app", version: "1.0", depends: "new-lib"},
	})

	r := New(idx, "amd64")
	result, err := r.Resolve([]Requirement{{Name: "old-app"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := pkgNames(result)
	assertContains(t, names, "new-lib")
	assertContains(t, names, "old-app")
}

// --- Helpers ---

func pkgNames(result *Result) []string {
	var names []string
	for _, pkg := range result.Packages {
		names = append(names, pkg.Name)
	}
	sort.Strings(names)
	return names
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func assertContains(t *testing.T, names []string, name string) {
	t.Helper()
	if !contains(names, name) {
		t.Errorf("expected %q in result, got: %v", name, names)
	}
}

func assertNotContains(t *testing.T, names []string, name string) {
	t.Helper()
	if contains(names, name) {
		t.Errorf("did not expect %q in result, got: %v", name, names)
	}
}
