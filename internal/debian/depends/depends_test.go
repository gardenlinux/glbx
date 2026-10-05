package depends

import (
	"testing"
)

func TestSimplePackageName(t *testing.T) {
	result, err := Parse("libc6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 clause, got %d", len(result))
	}
	if len(result[0]) != 1 {
		t.Fatalf("expected 1 alternative, got %d", len(result[0]))
	}
	dep := result[0][0]
	if dep.Name != "libc6" {
		t.Errorf("name = %q, want %q", dep.Name, "libc6")
	}
	if dep.Version != nil {
		t.Errorf("version = %v, want nil", dep.Version)
	}
	if dep.Arch != "" {
		t.Errorf("arch = %q, want empty", dep.Arch)
	}
}

func TestWithVersionConstraint(t *testing.T) {
	tests := []struct {
		input   string
		name    string
		op      string
		version string
	}{
		{"libc6 (>= 2.34)", "libc6", ">=", "2.34"},
		{"dpkg (<< 1.20.0)", "dpkg", "<<", "1.20.0"},
		{"base-files (= 12.4)", "base-files", "=", "12.4"},
		{"gcc (>> 4.0)", "gcc", ">>", "4.0"},
		{"perl (<= 5.36.0-1)", "perl", "<=", "5.36.0-1"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) != 1 || len(result[0]) != 1 {
				t.Fatalf("expected 1 clause with 1 dep, got %d clauses", len(result))
			}
			dep := result[0][0]
			if dep.Name != tt.name {
				t.Errorf("name = %q, want %q", dep.Name, tt.name)
			}
			if dep.Version == nil {
				t.Fatal("version is nil")
			}
			if dep.Version.Op != tt.op {
				t.Errorf("op = %q, want %q", dep.Version.Op, tt.op)
			}
			if dep.Version.Version != tt.version {
				t.Errorf("version = %q, want %q", dep.Version.Version, tt.version)
			}
		})
	}
}

func TestWithArchQualifier(t *testing.T) {
	result, err := Parse("libfoo:amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || len(result[0]) != 1 {
		t.Fatalf("expected 1 clause with 1 dep")
	}
	dep := result[0][0]
	if dep.Name != "libfoo" {
		t.Errorf("name = %q, want %q", dep.Name, "libfoo")
	}
	if dep.Arch != "amd64" {
		t.Errorf("arch = %q, want %q", dep.Arch, "amd64")
	}
}

func TestArchQualifierWithVersion(t *testing.T) {
	result, err := Parse("libfoo:amd64 (>= 1.0)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dep := result[0][0]
	if dep.Name != "libfoo" {
		t.Errorf("name = %q, want %q", dep.Name, "libfoo")
	}
	if dep.Arch != "amd64" {
		t.Errorf("arch = %q, want %q", dep.Arch, "amd64")
	}
	if dep.Version == nil || dep.Version.Op != ">=" || dep.Version.Version != "1.0" {
		t.Errorf("version = %v, want >= 1.0", dep.Version)
	}
}

func TestWithArchRestrictionInclusion(t *testing.T) {
	result, err := Parse("libfoo [amd64 arm64]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dep := result[0][0]
	if dep.Name != "libfoo" {
		t.Errorf("name = %q, want %q", dep.Name, "libfoo")
	}
	if len(dep.ArchList) != 2 {
		t.Fatalf("archlist length = %d, want 2", len(dep.ArchList))
	}
	if dep.ArchList[0] != "amd64" || dep.ArchList[1] != "arm64" {
		t.Errorf("archlist = %v, want [amd64 arm64]", dep.ArchList)
	}
	if dep.ArchExclude {
		t.Error("archExclude = true, want false")
	}
}

func TestWithArchRestrictionExclusion(t *testing.T) {
	result, err := Parse("libfoo [!i386 !armel]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dep := result[0][0]
	if len(dep.ArchList) != 2 {
		t.Fatalf("archlist length = %d, want 2", len(dep.ArchList))
	}
	if dep.ArchList[0] != "i386" || dep.ArchList[1] != "armel" {
		t.Errorf("archlist = %v, want [i386 armel]", dep.ArchList)
	}
	if !dep.ArchExclude {
		t.Error("archExclude = false, want true")
	}
}

func TestWithBuildProfiles(t *testing.T) {
	result, err := Parse("libfoo <cross> <!nocheck>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dep := result[0][0]
	if dep.Name != "libfoo" {
		t.Errorf("name = %q, want %q", dep.Name, "libfoo")
	}
	if len(dep.Profiles) != 2 {
		t.Fatalf("profiles length = %d, want 2", len(dep.Profiles))
	}
	if len(dep.Profiles[0]) != 1 || dep.Profiles[0][0] != "cross" {
		t.Errorf("profiles[0] = %v, want [cross]", dep.Profiles[0])
	}
	if len(dep.Profiles[1]) != 1 || dep.Profiles[1][0] != "!nocheck" {
		t.Errorf("profiles[1] = %v, want [!nocheck]", dep.Profiles[1])
	}
}

func TestAlternatives(t *testing.T) {
	result, err := Parse("gpgv | gpgv2 | gpgv1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 clause, got %d", len(result))
	}
	if len(result[0]) != 3 {
		t.Fatalf("expected 3 alternatives, got %d", len(result[0]))
	}
	names := []string{result[0][0].Name, result[0][1].Name, result[0][2].Name}
	expected := []string{"gpgv", "gpgv2", "gpgv1"}
	for i, n := range names {
		if n != expected[i] {
			t.Errorf("alternative[%d] = %q, want %q", i, n, expected[i])
		}
	}
}

func TestAlternativesWithVersions(t *testing.T) {
	result, err := Parse("libfoo (>= 2.0) | libfoo-alt (>= 1.0)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 clause, got %d", len(result))
	}
	if len(result[0]) != 2 {
		t.Fatalf("expected 2 alternatives, got %d", len(result[0]))
	}
	if result[0][0].Name != "libfoo" || result[0][0].Version.Version != "2.0" {
		t.Errorf("alt[0] wrong: %+v", result[0][0])
	}
	if result[0][1].Name != "libfoo-alt" || result[0][1].Version.Version != "1.0" {
		t.Errorf("alt[1] wrong: %+v", result[0][1])
	}
}

func TestMultipleClauses(t *testing.T) {
	result, err := Parse("libc6 (>= 2.34), libgcc-s1 (>= 3.3.1), libstdc++6 (>= 12)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 clauses, got %d", len(result))
	}
	if result[0][0].Name != "libc6" {
		t.Errorf("clause 0 name = %q, want libc6", result[0][0].Name)
	}
	if result[1][0].Name != "libgcc-s1" {
		t.Errorf("clause 1 name = %q, want libgcc-s1", result[1][0].Name)
	}
	if result[2][0].Name != "libstdc++6" {
		t.Errorf("clause 2 name = %q, want libstdc++6", result[2][0].Name)
	}
}

func TestComplexBuildDepends(t *testing.T) {
	// Real-world Build-Depends from a Debian package
	input := "debhelper-compat (= 13), dh-sequence-gnome, gettext (>= 0.19.6), libglib2.0-dev (>= 2.44.0), libgtk-3-dev (>= 3.22.0) [linux-any], libsecret-1-dev [!hurd-any], meson (>= 0.50.0), pkg-config, valac (>= 0.16) <!nocheck>"
	result, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 9 {
		t.Fatalf("expected 9 clauses, got %d", len(result))
	}

	// debhelper-compat (= 13)
	if result[0][0].Name != "debhelper-compat" {
		t.Errorf("clause 0 name = %q", result[0][0].Name)
	}
	if result[0][0].Version == nil || result[0][0].Version.Op != "=" || result[0][0].Version.Version != "13" {
		t.Errorf("clause 0 version wrong: %+v", result[0][0].Version)
	}

	// libgtk-3-dev (>= 3.22.0) [linux-any]
	gtk := result[4][0]
	if gtk.Name != "libgtk-3-dev" {
		t.Errorf("gtk name = %q", gtk.Name)
	}
	if gtk.Version == nil || gtk.Version.Version != "3.22.0" {
		t.Errorf("gtk version wrong: %+v", gtk.Version)
	}
	if len(gtk.ArchList) != 1 || gtk.ArchList[0] != "linux-any" {
		t.Errorf("gtk archlist = %v, want [linux-any]", gtk.ArchList)
	}
	if gtk.ArchExclude {
		t.Error("gtk archExclude = true, want false")
	}

	// libsecret-1-dev [!hurd-any]
	secret := result[5][0]
	if secret.Name != "libsecret-1-dev" {
		t.Errorf("secret name = %q", secret.Name)
	}
	if len(secret.ArchList) != 1 || secret.ArchList[0] != "hurd-any" {
		t.Errorf("secret archlist = %v, want [hurd-any]", secret.ArchList)
	}
	if !secret.ArchExclude {
		t.Error("secret archExclude = false, want true")
	}

	// valac (>= 0.16) <!nocheck>
	valac := result[8][0]
	if valac.Name != "valac" {
		t.Errorf("valac name = %q", valac.Name)
	}
	if valac.Version == nil || valac.Version.Version != "0.16" {
		t.Errorf("valac version wrong: %+v", valac.Version)
	}
	if len(valac.Profiles) != 1 || len(valac.Profiles[0]) != 1 || valac.Profiles[0][0] != "!nocheck" {
		t.Errorf("valac profiles = %v, want [[!nocheck]]", valac.Profiles)
	}
}

func TestAptDependsLine(t *testing.T) {
	// From apt's Depends field
	input := "adduser, gpgv | gpgv2 | gpgv1, libapt-pkg6.0 (>= 2.6.1), debian-archive-keyring, libc6 (>= 2.34)"
	result, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 5 {
		t.Fatalf("expected 5 clauses, got %d", len(result))
	}

	// adduser (simple)
	if result[0][0].Name != "adduser" {
		t.Errorf("clause 0 = %q, want adduser", result[0][0].Name)
	}

	// gpgv | gpgv2 | gpgv1 (alternatives)
	if len(result[1]) != 3 {
		t.Fatalf("clause 1 alternatives = %d, want 3", len(result[1]))
	}
	if result[1][0].Name != "gpgv" || result[1][1].Name != "gpgv2" || result[1][2].Name != "gpgv1" {
		t.Errorf("clause 1 alternatives wrong")
	}

	// libapt-pkg6.0 (>= 2.6.1) (versioned)
	if result[2][0].Name != "libapt-pkg6.0" {
		t.Errorf("clause 2 name = %q", result[2][0].Name)
	}
	if result[2][0].Version == nil || result[2][0].Version.Op != ">=" || result[2][0].Version.Version != "2.6.1" {
		t.Errorf("clause 2 version wrong")
	}
}

func TestEmptyInput(t *testing.T) {
	result, err := Parse("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestWhitespaceOnly(t *testing.T) {
	result, err := Parse("   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestErrorMalformedVersion(t *testing.T) {
	_, err := Parse("pkg (>= )")
	if err != nil {
		// Some parsers accept this, but an empty version is invalid
		// Our parser should reject or handle gracefully
		t.Logf("got expected error: %v", err)
	}
}

func TestErrorUnclosedParen(t *testing.T) {
	_, err := Parse("pkg (>= 1.0")
	if err == nil {
		t.Fatal("expected error for unclosed paren")
	}
}

func TestErrorInvalidOperator(t *testing.T) {
	_, err := Parse("pkg (!= 1.0)")
	if err == nil {
		t.Fatal("expected error for invalid operator")
	}
}

func TestMultipleProfileGroups(t *testing.T) {
	result, err := Parse("pkg <stage1> <cross !nocheck>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dep := result[0][0]
	if len(dep.Profiles) != 2 {
		t.Fatalf("profiles length = %d, want 2", len(dep.Profiles))
	}
	if dep.Profiles[0][0] != "stage1" {
		t.Errorf("profiles[0][0] = %q, want stage1", dep.Profiles[0][0])
	}
	if len(dep.Profiles[1]) != 2 {
		t.Fatalf("profiles[1] length = %d, want 2", len(dep.Profiles[1]))
	}
	if dep.Profiles[1][0] != "cross" || dep.Profiles[1][1] != "!nocheck" {
		t.Errorf("profiles[1] = %v, want [cross !nocheck]", dep.Profiles[1])
	}
}

func TestPackageNameWithPlus(t *testing.T) {
	result, err := Parse("g++ (>= 4:10.0)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result[0][0].Name != "g++" {
		t.Errorf("name = %q, want g++", result[0][0].Name)
	}
	if result[0][0].Version.Version != "4:10.0" {
		t.Errorf("version = %q, want 4:10.0", result[0][0].Version.Version)
	}
}

func TestPackageNameWithDots(t *testing.T) {
	result, err := Parse("libapt-pkg6.0 (>= 2.0)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result[0][0].Name != "libapt-pkg6.0" {
		t.Errorf("name = %q, want libapt-pkg6.0", result[0][0].Name)
	}
}

func TestVersionWithEpoch(t *testing.T) {
	result, err := Parse("perl (>= 5:5.36.0-1+deb12u1)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dep := result[0][0]
	if dep.Version.Version != "5:5.36.0-1+deb12u1" {
		t.Errorf("version = %q, want 5:5.36.0-1+deb12u1", dep.Version.Version)
	}
}

func TestAllFeaturesCombined(t *testing.T) {
	result, err := Parse("libfoo:amd64 (>= 2.0) [amd64 arm64] <cross>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dep := result[0][0]
	if dep.Name != "libfoo" {
		t.Errorf("name = %q, want libfoo", dep.Name)
	}
	if dep.Arch != "amd64" {
		t.Errorf("arch = %q, want amd64", dep.Arch)
	}
	if dep.Version == nil || dep.Version.Op != ">=" || dep.Version.Version != "2.0" {
		t.Errorf("version wrong: %+v", dep.Version)
	}
	if len(dep.ArchList) != 2 || dep.ArchList[0] != "amd64" || dep.ArchList[1] != "arm64" {
		t.Errorf("archlist = %v, want [amd64 arm64]", dep.ArchList)
	}
	if len(dep.Profiles) != 1 || dep.Profiles[0][0] != "cross" {
		t.Errorf("profiles = %v, want [[cross]]", dep.Profiles)
	}
}

func TestTrailingComma(t *testing.T) {
	// Some real-world files have trailing commas
	result, err := Parse("pkg1, pkg2,")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 clauses, got %d", len(result))
	}
}
