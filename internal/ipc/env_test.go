package ipc

import (
	"reflect"
	"testing"
)

func TestResolveEnv(t *testing.T) {
	inherited := []string{"PATH=/host", "HOME=/h", "LANG=C"}

	tests := []struct {
		name    string
		reset   bool
		overlay []string
		want    []string
	}{
		{
			name:    "no_reset_no_overlay_returns_inherited",
			reset:   false,
			overlay: nil,
			want:    inherited,
		},
		{
			name:    "reset_no_overlay_returns_default_path_only",
			reset:   true,
			overlay: nil,
			want:    []string{"PATH=" + DefaultPATH},
		},
		{
			name:    "no_reset_overlay_appends_new_key",
			reset:   false,
			overlay: []string{"FOO=bar"},
			want:    []string{"PATH=/host", "HOME=/h", "LANG=C", "FOO=bar"},
		},
		{
			name:    "no_reset_overlay_overrides_existing_in_place",
			reset:   false,
			overlay: []string{"PATH=/x"},
			want:    []string{"PATH=/x", "HOME=/h", "LANG=C"},
		},
		{
			name:    "reset_overlay_overrides_default_path",
			reset:   true,
			overlay: []string{"PATH=/x"},
			want:    []string{"PATH=/x"},
		},
		{
			name:    "reset_overlay_appends_after_default_path",
			reset:   true,
			overlay: []string{"FOO=bar"},
			want:    []string{"PATH=" + DefaultPATH, "FOO=bar"},
		},
		{
			name:    "malformed_entry_without_eq_is_dropped",
			reset:   false,
			overlay: []string{"BAD", "OK=1"},
			want:    []string{"PATH=/host", "HOME=/h", "LANG=C", "OK=1"},
		},
		{
			name:    "overlay_dup_last_wins",
			reset:   false,
			overlay: []string{"FOO=1", "FOO=2"},
			want:    []string{"PATH=/host", "HOME=/h", "LANG=C", "FOO=2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveEnv(tc.reset, tc.overlay, inherited)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ResolveEnv(reset=%v, overlay=%v):\ngot:  %v\nwant: %v", tc.reset, tc.overlay, got, tc.want)
			}
		})
	}
}

func TestResolveEnv_NoOverlayReturnsInheritedDirectly(t *testing.T) {
	// When overlay is empty and reset is false, returning the inherited slice
	// directly is a documented behavior — no allocation.
	inherited := []string{"A=1"}
	got := ResolveEnv(false, nil, inherited)
	if &got[0] != &inherited[0] {
		t.Errorf("expected ResolveEnv to return inherited slice directly when overlay is empty")
	}
}
