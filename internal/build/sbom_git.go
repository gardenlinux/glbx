package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitSourceRef identifies a packaging directory by version-control coordinates
// for an SBOM vcs externalRef: the repository URL, the commit, and the
// repo-relative subdirectory. It is best-effort — outside a git checkout the repo
// falls back to the absolute toplevel path and the commit may be empty.
type gitSourceRef struct {
	repo   string // repository URL (or absolute path fallback)
	commit string // HEAD commit, or "" if unavailable
	subdir string // repo-relative path of the packaging dir (e.g. pkgs/openssl/src/debian)
}

// resolveGitSourceRef derives the VCS coordinates of a package's src/debian
// directory, following the selection rule from the SBOM concept chapter:
//
//   - repository: $GLBX_SBOM_SOURCE_REPO if set; else the current repo's origin
//     URL; else the absolute path of the repository root.
//   - commit: the repository's HEAD (empty when not a git checkout).
//   - subdir: the packaging dir relative to the repository root; when there is no
//     git root, the absolute packaging path.
//
// pkgDir is the absolute pkgs/<name> directory; the packaging dir is
// pkgDir/src/debian.
func resolveGitSourceRef(pkgDir string) gitSourceRef {
	packagingDir := filepath.Join(pkgDir, "src", "debian")

	toplevel := gitOutput(pkgDir, "rev-parse", "--show-toplevel")
	commit := gitOutput(pkgDir, "rev-parse", "HEAD")

	ref := gitSourceRef{commit: commit}

	if override := os.Getenv("GLBX_SBOM_SOURCE_REPO"); override != "" {
		ref.repo = override
	} else if origin := gitOutput(pkgDir, "config", "--get", "remote.origin.url"); origin != "" {
		ref.repo = origin
	} else if toplevel != "" {
		ref.repo = toplevel
	} else {
		ref.repo = packagingDir
	}

	if toplevel != "" {
		if rel, err := filepath.Rel(toplevel, packagingDir); err == nil {
			ref.subdir = filepath.ToSlash(rel)
		}
	}
	// Outside a git checkout there is no repo-relative subdir; the repo field (the
	// absolute packaging path) already identifies the directory, so subdir stays
	// empty rather than duplicating it.
	return ref
}

// gitOutput runs `git -C dir <args...>` and returns trimmed stdout, or "" on any
// error (not a git checkout, git absent, etc.).
func gitOutput(dir string, args ...string) string {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
