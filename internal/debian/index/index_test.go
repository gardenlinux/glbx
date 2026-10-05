package index

import (
	"os"
	"strings"
	"testing"
)

func mustReadFixture(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return string(b)
}

var testPackagesFile = mustReadFixture("testdata/packages_basic.txt")

func TestLoadBasic(t *testing.T) {
	idx, err := Load(strings.NewReader(testPackagesFile))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if idx.Len() != 4 {
		t.Errorf("Len() = %d, want 4", idx.Len())
	}
}

func TestGet(t *testing.T) {
	idx, err := Load(strings.NewReader(testPackagesFile))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	pkg := idx.Get("coreutils")
	if pkg == nil {
		t.Fatal("Get(coreutils) returned nil")
	}
	if pkg.Name != "coreutils" {
		t.Errorf("Name = %q, want %q", pkg.Name, "coreutils")
	}
	if pkg.Version != "9.1-1" {
		t.Errorf("Version = %q, want %q", pkg.Version, "9.1-1")
	}
	if pkg.Architecture != "amd64" {
		t.Errorf("Architecture = %q, want %q", pkg.Architecture, "amd64")
	}
	if !pkg.Essential {
		t.Error("Essential = false, want true")
	}
	if pkg.Priority != "required" {
		t.Errorf("Priority = %q, want %q", pkg.Priority, "required")
	}
	if pkg.SHA256 != "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890" {
		t.Errorf("SHA256 = %q, want abcdef...", pkg.SHA256)
	}
	if pkg.Filename != "pool/main/c/coreutils/coreutils_9.1-1_amd64.deb" {
		t.Errorf("Filename = %q", pkg.Filename)
	}
	if pkg.Size != 8123456 {
		t.Errorf("Size = %d, want 8123456", pkg.Size)
	}
}

func TestGetNotFound(t *testing.T) {
	idx, err := Load(strings.NewReader(testPackagesFile))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if pkg := idx.Get("nonexistent"); pkg != nil {
		t.Errorf("Get(nonexistent) = %v, want nil", pkg)
	}
}

func TestHas(t *testing.T) {
	idx, err := Load(strings.NewReader(testPackagesFile))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if !idx.Has("bash") {
		t.Error("Has(bash) = false, want true")
	}
	if idx.Has("nonexistent") {
		t.Error("Has(nonexistent) = true, want false")
	}
}

func TestDependencyParsing(t *testing.T) {
	idx, err := Load(strings.NewReader(testPackagesFile))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	pkg := idx.Get("coreutils")
	if pkg == nil {
		t.Fatal("Get(coreutils) returned nil")
	}

	// Depends: libc6 (>= 2.34), libselinux1 (>= 3.1~)
	if len(pkg.Depends) != 2 {
		t.Fatalf("len(Depends) = %d, want 2", len(pkg.Depends))
	}
	if pkg.Depends[0][0].Name != "libc6" {
		t.Errorf("Depends[0][0].Name = %q, want libc6", pkg.Depends[0][0].Name)
	}
	if pkg.Depends[0][0].Version == nil || pkg.Depends[0][0].Version.Op != ">=" || pkg.Depends[0][0].Version.Version != "2.34" {
		t.Errorf("Depends[0][0].Version = %+v, want >= 2.34", pkg.Depends[0][0].Version)
	}
	if pkg.Depends[1][0].Name != "libselinux1" {
		t.Errorf("Depends[1][0].Name = %q, want libselinux1", pkg.Depends[1][0].Name)
	}

	// Pre-Depends: libacl1 (>= 2.2.23)
	if len(pkg.PreDepends) != 1 {
		t.Fatalf("len(PreDepends) = %d, want 1", len(pkg.PreDepends))
	}
	if pkg.PreDepends[0][0].Name != "libacl1" {
		t.Errorf("PreDepends[0][0].Name = %q, want libacl1", pkg.PreDepends[0][0].Name)
	}

	// Conflicts: timeout
	if len(pkg.Conflicts) != 1 {
		t.Fatalf("len(Conflicts) = %d, want 1", len(pkg.Conflicts))
	}
	if pkg.Conflicts[0][0].Name != "timeout" {
		t.Errorf("Conflicts[0][0].Name = %q, want timeout", pkg.Conflicts[0][0].Name)
	}

	// Provides: textutils, shellutils, fileutils
	if len(pkg.Provides) != 3 {
		t.Fatalf("len(Provides) = %d, want 3", len(pkg.Provides))
	}
}

func TestBreaks(t *testing.T) {
	idx, err := Load(strings.NewReader(testPackagesFile))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	pkg := idx.Get("bash")
	if pkg == nil {
		t.Fatal("Get(bash) returned nil")
	}

	// Breaks: bash-completion (<< 1:2.1-4.1~)
	if len(pkg.Breaks) != 1 {
		t.Fatalf("len(Breaks) = %d, want 1", len(pkg.Breaks))
	}
	if pkg.Breaks[0][0].Name != "bash-completion" {
		t.Errorf("Breaks[0][0].Name = %q, want bash-completion", pkg.Breaks[0][0].Name)
	}
	if pkg.Breaks[0][0].Version == nil || pkg.Breaks[0][0].Version.Op != "<<" {
		t.Errorf("Breaks[0][0].Version = %+v, want << ...", pkg.Breaks[0][0].Version)
	}
}

func TestProviders(t *testing.T) {
	idx, err := Load(strings.NewReader(testPackagesFile))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// coreutils provides textutils, shellutils, fileutils
	providers := idx.Providers("textutils")
	if len(providers) != 1 {
		t.Fatalf("Providers(textutils) has %d entries, want 1", len(providers))
	}
	if providers[0].Name != "coreutils" {
		t.Errorf("Providers(textutils)[0].Name = %q, want coreutils", providers[0].Name)
	}

	providers = idx.Providers("shellutils")
	if len(providers) != 1 || providers[0].Name != "coreutils" {
		t.Error("Providers(shellutils) did not return coreutils")
	}

	// virtual-provider provides mail-transport-agent, smtp-server (= 2.0)
	providers = idx.Providers("mail-transport-agent")
	if len(providers) != 1 || providers[0].Name != "virtual-provider" {
		t.Error("Providers(mail-transport-agent) did not return virtual-provider")
	}

	providers = idx.Providers("smtp-server")
	if len(providers) != 1 || providers[0].Name != "virtual-provider" {
		t.Error("Providers(smtp-server) did not return virtual-provider")
	}

	// Nonexistent virtual package
	providers = idx.Providers("nonexistent-virtual")
	if len(providers) != 0 {
		t.Errorf("Providers(nonexistent-virtual) has %d entries, want 0", len(providers))
	}
}

func TestProvidesWithVersion(t *testing.T) {
	idx, err := Load(strings.NewReader(testPackagesFile))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// libc6 provides: libc6-amd64 (= 2.36-9)
	providers := idx.Providers("libc6-amd64")
	if len(providers) != 1 {
		t.Fatalf("Providers(libc6-amd64) has %d entries, want 1", len(providers))
	}
	if providers[0].Name != "libc6" {
		t.Errorf("provider name = %q, want libc6", providers[0].Name)
	}
}

func TestEssentialPackages(t *testing.T) {
	idx, err := Load(strings.NewReader(testPackagesFile))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	ess := idx.EssentialPackages()
	// coreutils: Essential: yes — included
	// libc6: Priority: required (NOT essential)
	// bash: Priority: important (NOT essential)
	// virtual-provider: Priority: optional (NOT essential)
	if len(ess) != 1 {
		t.Fatalf("EssentialPackages() returned %d packages, want 1", len(ess))
	}
	if ess[0].Name != "coreutils" {
		t.Errorf("EssentialPackages()[0] = %q, want coreutils", ess[0].Name)
	}
}

func TestAll(t *testing.T) {
	idx, err := Load(strings.NewReader(testPackagesFile))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	all := idx.All()
	if len(all) != 4 {
		t.Errorf("All() returned %d packages, want 4", len(all))
	}

	// Verify it's a copy (modifying returned slice doesn't affect index)
	all[0] = nil
	if idx.Get("coreutils") == nil {
		t.Error("modifying All() slice affected the index")
	}
}

func TestMerge(t *testing.T) {
	base := mustReadFixture("testdata/merge_base.txt")
	overlay := mustReadFixture("testdata/merge_overlay.txt")

	idxBase, err := Load(strings.NewReader(base))
	if err != nil {
		t.Fatalf("Load(base) failed: %v", err)
	}

	idxOverlay, err := Load(strings.NewReader(overlay))
	if err != nil {
		t.Fatalf("Load(overlay) failed: %v", err)
	}

	merged := idxBase.Merge(idxOverlay)

	// Should have 3 unique packages: foo (overridden), bar, baz
	if merged.Len() != 3 {
		t.Errorf("merged.Len() = %d, want 3", merged.Len())
	}

	// foo should be the overridden version
	foo := merged.Get("foo")
	if foo == nil {
		t.Fatal("merged.Get(foo) returned nil")
	}
	if foo.Version != "1.1-1" {
		t.Errorf("merged foo.Version = %q, want 1.1-1", foo.Version)
	}

	// bar should still exist
	bar := merged.Get("bar")
	if bar == nil {
		t.Fatal("merged.Get(bar) returned nil")
	}
	if bar.Version != "2.0-1" {
		t.Errorf("merged bar.Version = %q, want 2.0-1", bar.Version)
	}

	// baz should be added
	baz := merged.Get("baz")
	if baz == nil {
		t.Fatal("merged.Get(baz) returned nil")
	}
	if baz.Version != "3.0-1" {
		t.Errorf("merged baz.Version = %q, want 3.0-1", baz.Version)
	}

	// Original indices should be unmodified
	if idxBase.Get("foo").Version != "1.0-1" {
		t.Error("base index was modified by Merge")
	}
	if idxBase.Get("baz") != nil {
		t.Error("base index gained baz from Merge")
	}
}

func TestMergeProviders(t *testing.T) {
	base := `Package: foo
Version: 1.0-1
Architecture: amd64
Provides: virtual-x
Filename: pool/foo.deb
Size: 100
SHA256: aaaa
`

	overlay := `Package: bar
Version: 2.0-1
Architecture: amd64
Provides: virtual-x
Filename: pool/bar.deb
Size: 200
SHA256: bbbb
`

	idxBase, err := Load(strings.NewReader(base))
	if err != nil {
		t.Fatalf("Load(base) failed: %v", err)
	}

	idxOverlay, err := Load(strings.NewReader(overlay))
	if err != nil {
		t.Fatalf("Load(overlay) failed: %v", err)
	}

	merged := idxBase.Merge(idxOverlay)

	// Both foo and bar should provide virtual-x
	providers := merged.Providers("virtual-x")
	if len(providers) != 2 {
		t.Fatalf("Providers(virtual-x) has %d entries, want 2", len(providers))
	}

	names := make(map[string]bool)
	for _, p := range providers {
		names[p.Name] = true
	}
	if !names["foo"] || !names["bar"] {
		t.Errorf("Providers(virtual-x) = %v, want foo and bar", names)
	}
}

func TestEmptyIndex(t *testing.T) {
	idx, err := Load(strings.NewReader(""))
	if err != nil {
		t.Fatalf("Load(empty) failed: %v", err)
	}

	if idx.Len() != 0 {
		t.Errorf("Len() = %d, want 0", idx.Len())
	}
	if idx.Get("foo") != nil {
		t.Error("Get on empty index returned non-nil")
	}
	if idx.Has("foo") {
		t.Error("Has on empty index returned true")
	}
	if len(idx.Providers("foo")) != 0 {
		t.Error("Providers on empty index returned non-empty")
	}
	if len(idx.EssentialPackages()) != 0 {
		t.Error("EssentialPackages on empty index returned non-empty")
	}
	if len(idx.All()) != 0 {
		t.Error("All on empty index returned non-empty")
	}
}

func TestMissingOptionalFields(t *testing.T) {
	// Minimal valid package - only Package and Version are truly needed.
	input := `Package: minimal-pkg
Version: 0.1
Architecture: all
Filename: pool/minimal.deb
Size: 100
SHA256: eeee
`

	idx, err := Load(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	pkg := idx.Get("minimal-pkg")
	if pkg == nil {
		t.Fatal("Get(minimal-pkg) returned nil")
	}
	if pkg.Essential {
		t.Error("Essential should be false for package without Essential field")
	}
	if pkg.Priority != "" {
		t.Errorf("Priority = %q, want empty", pkg.Priority)
	}
	if pkg.Depends != nil {
		t.Errorf("Depends = %v, want nil", pkg.Depends)
	}
	if pkg.PreDepends != nil {
		t.Errorf("PreDepends = %v, want nil", pkg.PreDepends)
	}
	if pkg.Conflicts != nil {
		t.Errorf("Conflicts = %v, want nil", pkg.Conflicts)
	}
	if pkg.Provides != nil {
		t.Errorf("Provides = %v, want nil", pkg.Provides)
	}
	if pkg.Breaks != nil {
		t.Errorf("Breaks = %v, want nil", pkg.Breaks)
	}
}

func TestMissingPackageField(t *testing.T) {
	input := `Version: 1.0
Architecture: amd64
`

	_, err := Load(strings.NewReader(input))
	if err == nil {
		t.Fatal("Load() should fail on stanza without Package field")
	}
	if !strings.Contains(err.Error(), "Package") {
		t.Errorf("error %q does not mention Package field", err.Error())
	}
}

func TestRealWorldStanza(t *testing.T) {
	// Real-world-style stanza with multi-line dependency, alternatives.
	input := mustReadFixture("testdata/real_world_apt.txt")

	idx, err := Load(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	pkg := idx.Get("apt")
	if pkg == nil {
		t.Fatal("Get(apt) returned nil")
	}

	if !pkg.Essential {
		t.Error("apt should be Essential")
	}
	if pkg.Priority != "required" {
		t.Errorf("Priority = %q, want required", pkg.Priority)
	}

	// Depends has 10 clauses: adduser, gpgv|gpgv2|gpgv1, libapt-pkg6.0, ...
	if len(pkg.Depends) != 10 {
		t.Errorf("len(Depends) = %d, want 10", len(pkg.Depends))
	}

	// Check alternatives: gpgv | gpgv2 | gpgv1
	if len(pkg.Depends) >= 2 {
		altClause := pkg.Depends[1]
		if len(altClause) != 3 {
			t.Errorf("alternatives clause len = %d, want 3", len(altClause))
		} else {
			if altClause[0].Name != "gpgv" || altClause[1].Name != "gpgv2" || altClause[2].Name != "gpgv1" {
				t.Errorf("alternatives = %v/%v/%v, want gpgv/gpgv2/gpgv1",
					altClause[0].Name, altClause[1].Name, altClause[2].Name)
			}
		}
	}

	// Provides: apt-transport-https (= 2.6.1)
	providers := idx.Providers("apt-transport-https")
	if len(providers) != 1 || providers[0].Name != "apt" {
		t.Error("apt should be a provider of apt-transport-https")
	}

	// Essential packages should include apt (this stanza has Essential: yes).
	ess := idx.EssentialPackages()
	if len(ess) != 1 || ess[0].Name != "apt" {
		t.Errorf("EssentialPackages() = %v, want [apt]", ess)
	}
}

func TestDuplicatePackageName(t *testing.T) {
	input := `Package: dup
Version: 1.0-1
Architecture: amd64
Filename: pool/dup_1.0.deb
Size: 100
SHA256: aaaa

Package: dup
Version: 2.0-1
Architecture: amd64
Filename: pool/dup_2.0.deb
Size: 200
SHA256: bbbb
`

	_, err := Load(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for duplicate package name, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate package") {
		t.Errorf("error should mention 'duplicate package', got: %v", err)
	}
}

func TestStanzaRetained(t *testing.T) {
	input := `Package: testpkg
Version: 1.0
Architecture: amd64
Description: A test package
 With a long description
 spanning multiple lines.
Filename: pool/testpkg.deb
Size: 999
SHA256: abcd
`

	idx, err := Load(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	pkg := idx.Get("testpkg")
	if pkg == nil {
		t.Fatal("Get(testpkg) returned nil")
	}

	// The raw stanza should be preserved.
	if pkg.Stanza["description"] == "" {
		t.Error("Stanza should retain the description field")
	}
	if !strings.Contains(pkg.Stanza["description"], "long description") {
		t.Errorf("Stanza description = %q, should contain 'long description'", pkg.Stanza["description"])
	}
}

func TestSizeParsingEdgeCases(t *testing.T) {
	// Non-numeric size should not cause an error, just be zero.
	input := `Package: badsize
Version: 1.0
Architecture: amd64
Filename: pool/badsize.deb
Size: notanumber
SHA256: abcd
`

	idx, err := Load(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	pkg := idx.Get("badsize")
	if pkg == nil {
		t.Fatal("Get(badsize) returned nil")
	}
	if pkg.Size != 0 {
		t.Errorf("Size = %d, want 0 for unparseable size", pkg.Size)
	}
}
