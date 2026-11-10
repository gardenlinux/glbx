package buildcfg

import "testing"

func TestSubstArch(t *testing.T) {
	cases := []struct {
		name string
		in   string
		arch string
		want string
	}{
		{"no placeholder is unchanged", "glibc:libc6", "arm64", "glibc:libc6"},
		{"single placeholder", "linux:linux-image-7.2+unreleased-cloud-%ARCH%", "arm64", "linux:linux-image-7.2+unreleased-cloud-arm64"},
		{"amd64 round-trips to the old hard-coded name", "linux:linux-image-7.2+unreleased-cloud-%ARCH%", "amd64", "linux:linux-image-7.2+unreleased-cloud-amd64"},
		{"multiple placeholders", "%ARCH%-%ARCH%", "arm64", "arm64-arm64"},
		{"empty string", "", "arm64", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SubstArch(tc.in, tc.arch); got != tc.want {
				t.Errorf("SubstArch(%q, %q) = %q, want %q", tc.in, tc.arch, got, tc.want)
			}
		})
	}
}

// On an arch where the placeholder is absent, SubstArch must be a true no-op so
// non-kernel refs (the overwhelming majority) are never perturbed.
func TestSubstArchNoOpForPlainRefs(t *testing.T) {
	refs := []string{"bash:bash", "util-linux:mount", "systemd:systemd-boot"}
	for _, r := range refs {
		if got := SubstArch(r, "amd64"); got != r {
			t.Errorf("SubstArch(%q) perturbed a plain ref to %q", r, got)
		}
	}
}
