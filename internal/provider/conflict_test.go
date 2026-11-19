package provider

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLookupConflictResolverDispatch checks that auto-conflict-resolution is
// selected by the same pkg_type dispatch as lockfile generation: the kernel has
// a strategy, plain Debian does not, and a missing or pkg_type-less build.yml is
// "no strategy" rather than an error.
func TestLookupConflictResolverDispatch(t *testing.T) {
	cases := []struct {
		name        string
		buildYML    string // "" means do not create pkgs/<name>/build.yml
		wantResolve bool
		wantErr     bool
	}{
		{name: "kernel", buildYML: "pkg_type: debian-kernel\n", wantResolve: true},
		{name: "plain-debian", buildYML: "pkg_type: debian\n", wantResolve: false},
		{name: "no-pkg-type", buildYML: "build_profiles: [nodoc]\n", wantResolve: false},
		{name: "no-build-yml", buildYML: "", wantResolve: false},
		{name: "unknown-pkg-type", buildYML: "pkg_type: bogus\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.buildYML != "" {
				pkgDir := filepath.Join(dir, "pkgs", tc.name)
				if err := os.MkdirAll(pkgDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(pkgDir, "build.yml"), []byte(tc.buildYML), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			resolver, err := LookupConflictResolver(OpContext{OutputDir: dir}, tc.name)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got resolver=%v", resolver)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantResolve && resolver == nil {
				t.Fatal("expected a conflict resolver, got nil")
			}
			if !tc.wantResolve && resolver != nil {
				t.Fatalf("expected no conflict resolver, got %T", resolver)
			}
		})
	}
}

// TestHasRemainingConflicts checks the low-level unmerged-index probe the
// auto-resolution path ends on: a clean tree reports no conflicts; a tree with a
// staged merge conflict reports one; git-adding the conflicted path clears it.
func TestHasRemainingConflicts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "f"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-q", "-b", "main")
	write("base\n")
	git("add", "f")
	git("commit", "-q", "-m", "base")

	// A clean tree: no unmerged entries.
	if remaining, err := hasRemainingConflicts(root); err != nil || remaining {
		t.Fatalf("clean tree: remaining=%v err=%v", remaining, err)
	}

	// Build two divergent branches that edit the same line, so merging conflicts.
	git("checkout", "-q", "-b", "side")
	write("side\n")
	git("commit", "-qam", "side")
	git("checkout", "-q", "main")
	write("main\n")
	git("commit", "-qam", "main")

	// Merge side; it conflicts and leaves an unmerged entry.
	cmd := exec.Command("git", "-C", root, "merge", "--no-edit", "side")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected the merge to conflict")
	}
	if remaining, err := hasRemainingConflicts(root); err != nil || !remaining {
		t.Fatalf("mid-conflict: remaining=%v err=%v (want true)", remaining, err)
	}

	// Resolve by overwriting and git-adding via the helper under test.
	write("resolved\n")
	if err := gitAdd(root, "f"); err != nil {
		t.Fatalf("gitAdd: %v", err)
	}
	if remaining, err := hasRemainingConflicts(root); err != nil || remaining {
		t.Fatalf("after resolve: remaining=%v err=%v (want false)", remaining, err)
	}
}
