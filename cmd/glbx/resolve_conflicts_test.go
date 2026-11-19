package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInitRepo makes a throwaway git repo with one commit, returning its root.
func gitInitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "seed"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "seed")
	git("commit", "-q", "-m", "seed")
	return root
}

func writePkgBuildYML(t *testing.T, root, pkg, content string) {
	t.Helper()
	dir := filepath.Join(root, "pkgs", pkg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolveConflictsNoStrategy: a package whose pkg_type has no
// auto-resolution strategy (plain debian) exits non-zero with a clear message,
// so CI can branch on the exit status to fall back to the draft-PR path. No
// container or network is needed for this path.
func TestResolveConflictsNoStrategy(t *testing.T) {
	root := gitInitRepo(t)
	writePkgBuildYML(t, root, "coreutils", "pkg_type: debian\n")

	out, err := run(t, nil, "resolve-conflicts", "--output", root, "coreutils")
	if err == nil {
		t.Fatalf("expected non-zero exit for a pkg_type with no strategy; output:\n%s", out)
	}
	if !strings.Contains(string(out), "no auto-resolution") {
		t.Errorf("expected a 'no auto-resolution' message, got:\n%s", out)
	}
}

// TestResolveConflictsNoBuildYML: a package with no build.yml (hence no
// pkg_type) is also "no strategy" and exits non-zero — never a crash.
func TestResolveConflictsNoBuildYML(t *testing.T) {
	root := gitInitRepo(t)

	out, err := run(t, nil, "resolve-conflicts", "--output", root, "ghost")
	if err == nil {
		t.Fatalf("expected non-zero exit when no build.yml exists; output:\n%s", out)
	}
	if !strings.Contains(string(out), "no auto-resolution") {
		t.Errorf("expected a 'no auto-resolution' message, got:\n%s", out)
	}
}

// TestResolveConflictsMissingArg: the command requires a package argument.
func TestResolveConflictsMissingArg(t *testing.T) {
	out, err := run(t, nil, "resolve-conflicts")
	if err == nil {
		t.Fatalf("expected non-zero exit with no package argument; output:\n%s", out)
	}
	if !strings.Contains(string(out), "usage:") {
		t.Errorf("expected usage text, got:\n%s", out)
	}
}
