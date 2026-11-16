package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestParseGitHubRemote covers the remote URL forms git emits for each
// transport, the ".git"/path tolerances, and the reject cases that must fall
// back to no auto default: a non-github.com host and an unparseable path.
func TestParseGitHubRemote(t *testing.T) {
	cases := []struct {
		remote      string
		owner, repo string
		ok          bool
	}{
		// scp-like ssh
		{"git@github.com:gardenlinux/glbf-staging.git", "gardenlinux", "glbf-staging", true},
		{"git@github.com:gardenlinux/glbf-staging", "gardenlinux", "glbf-staging", true},
		// ssh URL
		{"ssh://git@github.com/gardenlinux/glbf-staging.git", "gardenlinux", "glbf-staging", true},
		{"ssh://github.com/owner/repo", "owner", "repo", true},
		// https, with and without userinfo and .git
		{"https://github.com/gardenlinux/glbf-staging.git", "gardenlinux", "glbf-staging", true},
		{"https://github.com/gardenlinux/glbf-staging", "gardenlinux", "glbf-staging", true},
		{"https://x-access-token:tok@github.com/owner/repo.git", "owner", "repo", true},
		{"http://github.com/owner/repo.git", "owner", "repo", true},
		// git protocol
		{"git://github.com/owner/repo.git", "owner", "repo", true},

		// Not github.com: no auto default.
		{"git@gitlab.com:owner/repo.git", "", "", false},
		{"https://gitlab.com/owner/repo.git", "", "", false},
		{"https://ghe.example.com/owner/repo.git", "", "", false},
		// A host that merely ends in github.com must not match.
		{"git@notgithub.com:owner/repo.git", "", "", false},
		{"https://evilgithub.com/owner/repo.git", "", "", false},
		// Malformed / wrong depth.
		{"https://github.com/owner", "", "", false},
		{"https://github.com/owner/repo/extra", "", "", false},
		{"git@github.com:owner", "", "", false},
		{"", "", "", false},
		{"not a url", "", "", false},
	}
	for _, c := range cases {
		owner, repo, ok := parseGitHubRemote(c.remote)
		if ok != c.ok || owner != c.owner || repo != c.repo {
			t.Errorf("parseGitHubRemote(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.remote, owner, repo, ok, c.owner, c.repo, c.ok)
		}
	}
}

// TestAutoRegistryFromGit drives the end-to-end default resolution against a
// real throwaway git repo shaped like the working tree (a pkgs/ dir and a
// rootfs-*.yml so findConfDir accepts it), confirming a github.com origin
// becomes the ghcr.io/.../objstore default and a non-github origin yields "".
func TestAutoRegistryFromGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	makeTree := func(t *testing.T, originURL string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "pkgs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rootfs-base.yml"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run := func(args ...string) {
			cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		run("init", "-q")
		run("remote", "add", "origin", originURL)
		return dir
	}

	// findConfDir / autoRegistryFromGit resolve relative to the working
	// directory, so run each case from inside its throwaway tree.
	chdir := func(t *testing.T, dir string) {
		t.Helper()
		prev, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(prev) })
	}

	t.Run("github origin becomes ghcr default", func(t *testing.T) {
		dir := makeTree(t, "git@github.com:gardenlinux/glbf-staging.git")
		chdir(t, dir)
		if got, want := autoRegistryFromGit(), "ghcr.io/gardenlinux/glbf-staging/objstore"; got != want {
			t.Errorf("autoRegistryFromGit() = %q, want %q", got, want)
		}
	})

	t.Run("non-github origin yields no default", func(t *testing.T) {
		dir := makeTree(t, "git@gitlab.com:owner/repo.git")
		chdir(t, dir)
		if got := autoRegistryFromGit(); got != "" {
			t.Errorf("autoRegistryFromGit() = %q, want \"\"", got)
		}
	})

	t.Run("not a git repo yields no default", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "pkgs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rootfs-base.yml"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		chdir(t, dir)
		if got := autoRegistryFromGit(); got != "" {
			t.Errorf("autoRegistryFromGit() = %q, want \"\"", got)
		}
	})
}
