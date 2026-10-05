package depends

import "testing"

func TestArchMatches(t *testing.T) {
	tests := []struct {
		pattern string
		arch    string
		want    bool
	}{
		// "any" matches everything.
		{"any", "amd64", true},
		{"any", "i386", true},
		{"any", "linux-arm64", true},

		// Exact match.
		{"amd64", "amd64", true},
		{"i386", "amd64", false},
		{"arm64", "arm64", true},

		// linux-any matches every Linux arch (the only OS we currently
		// special-case in ArchMatches).
		{"linux-any", "amd64", true},
		{"linux-any", "i386", true},
		{"linux-any", "arm64", true},

		// Other "<os>-any" patterns are NOT linux — must not match.
		{"hurd-any", "amd64", false},
		{"kfreebsd-any", "amd64", false},

		// "any-<cpu>" matches any OS for that CPU. Current implementation
		// returns true for the entire prefix family without further
		// scrutiny, which is what callers depend on.
		{"any-amd64", "amd64", true},
		{"any-i386", "amd64", true},

		// Mismatch on a non-wildcard pattern.
		{"powerpc", "amd64", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_vs_"+tt.arch, func(t *testing.T) {
			if got := ArchMatches(tt.pattern, tt.arch); got != tt.want {
				t.Errorf("ArchMatches(%q, %q) = %v, want %v", tt.pattern, tt.arch, got, tt.want)
			}
		})
	}
}

func TestMatchesArch(t *testing.T) {
	tests := []struct {
		name string
		dep  Dependency
		arch string
		want bool
	}{
		{
			name: "no_arch_list_matches_any_arch",
			dep:  Dependency{Name: "libc6"},
			arch: "amd64",
			want: true,
		},
		{
			name: "include_list_match",
			dep:  Dependency{Name: "libfoo", ArchList: []string{"amd64", "arm64"}},
			arch: "amd64",
			want: true,
		},
		{
			name: "include_list_no_match",
			dep:  Dependency{Name: "libfoo", ArchList: []string{"amd64", "arm64"}},
			arch: "i386",
			want: false,
		},
		{
			name: "include_list_with_wildcard_match",
			dep:  Dependency{Name: "libfoo", ArchList: []string{"linux-any"}},
			arch: "amd64",
			want: true,
		},
		{
			name: "exclude_list_excludes_match",
			dep:  Dependency{Name: "libfoo", ArchList: []string{"i386"}, ArchExclude: true},
			arch: "i386",
			want: false,
		},
		{
			name: "exclude_list_keeps_non_match",
			dep:  Dependency{Name: "libfoo", ArchList: []string{"i386"}, ArchExclude: true},
			arch: "amd64",
			want: true,
		},
		{
			name: "exclude_list_with_wildcard_excludes",
			dep:  Dependency{Name: "libfoo", ArchList: []string{"linux-any"}, ArchExclude: true},
			arch: "amd64",
			want: false,
		},
		{
			name: "exclude_multiple_first_excludes",
			dep:  Dependency{Name: "libfoo", ArchList: []string{"i386", "armhf"}, ArchExclude: true},
			arch: "armhf",
			want: false,
		},
		{
			name: "exclude_multiple_none_match",
			dep:  Dependency{Name: "libfoo", ArchList: []string{"i386", "armhf"}, ArchExclude: true},
			arch: "amd64",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.dep.MatchesArch(tt.arch); got != tt.want {
				t.Errorf("MatchesArch(%q) = %v, want %v", tt.arch, got, tt.want)
			}
		})
	}
}

func TestExcludedByProfiles(t *testing.T) {
	tests := []struct {
		name           string
		profiles       [][]string
		activeProfiles []string
		wantExcluded   bool
	}{
		// No profile restriction → always kept.
		{
			name:           "no_profiles_means_kept",
			profiles:       nil,
			activeProfiles: []string{"cross"},
			wantExcluded:   false,
		},
		// Single positive profile group satisfied.
		{
			name:           "positive_profile_active",
			profiles:       [][]string{{"cross"}},
			activeProfiles: []string{"cross"},
			wantExcluded:   false,
		},
		// Single positive profile group NOT satisfied.
		{
			name:           "positive_profile_not_active",
			profiles:       [][]string{{"cross"}},
			activeProfiles: nil,
			wantExcluded:   true,
		},
		// Negated profile (!nocheck) is satisfied when nocheck is NOT active.
		{
			name:           "negated_profile_active_when_absent",
			profiles:       [][]string{{"!nocheck"}},
			activeProfiles: nil,
			wantExcluded:   false,
		},
		// Negated profile fails when the named profile IS active.
		{
			name:           "negated_profile_inactive_when_present",
			profiles:       [][]string{{"!nocheck"}},
			activeProfiles: []string{"nocheck"},
			wantExcluded:   true,
		},
		// AND within a group: BOTH terms must hold for the group to satisfy.
		{
			name:           "and_within_group_all_satisfied",
			profiles:       [][]string{{"cross", "!nocheck"}},
			activeProfiles: []string{"cross"},
			wantExcluded:   false,
		},
		{
			name:           "and_within_group_one_term_missing",
			profiles:       [][]string{{"cross", "stage1"}},
			activeProfiles: []string{"cross"},
			wantExcluded:   true,
		},
		// OR across groups: ANY group can satisfy.
		{
			name:           "or_across_groups_second_satisfies",
			profiles:       [][]string{{"cross"}, {"stage1"}},
			activeProfiles: []string{"stage1"},
			wantExcluded:   false,
		},
		{
			name:           "or_across_groups_none_satisfies",
			profiles:       [][]string{{"cross"}, {"stage1"}},
			activeProfiles: []string{"nocheck"},
			wantExcluded:   true,
		},
		// Empty active profiles is the dominant production case for
		// straight-up "is this dep ever wanted?" checks.
		{
			name:           "empty_active_excludes_all_positive",
			profiles:       [][]string{{"stage1"}},
			activeProfiles: nil,
			wantExcluded:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dep := Dependency{Profiles: tt.profiles}
			if got := dep.ExcludedByProfiles(tt.activeProfiles); got != tt.wantExcluded {
				t.Errorf("ExcludedByProfiles(%v) with profiles %v = %v, want %v",
					tt.activeProfiles, tt.profiles, got, tt.wantExcluded)
			}
		})
	}
}
