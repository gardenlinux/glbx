package importer

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
)

// Import commit metadata is carried in the commit message as a fenced block.
// The begin/end markers must match as exact, full lines (note the three spaces
// in the end marker). Between them is YAML recording the package identity, the
// imported version, and the auto-update source.
const (
	importMetaBegin = "---BEGIN PKG IMPORT METADATA---"
	importMetaEnd   = "---END   PKG IMPORT METADATA---"
)

// importCommit is a validated upstream import commit for a package: its commit
// hash and the version recorded in its metadata block.
type importCommit struct {
	Hash    string
	Version string
}

// isGitRepo reports whether root is inside a git working tree.
func isGitRepo(root string) bool {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree")
	return cmd.Run() == nil
}

// formatImportMessage builds the commit message for an import: a human subject
// line followed by the fenced metadata block.
func formatImportMessage(pkg, version, autoUpdate string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "import %s %s from debian\n\n", pkg, version)
	b.WriteString(importMetaBegin + "\n")
	fmt.Fprintf(&b, "pkg: %s\n", pkg)
	fmt.Fprintf(&b, "version: %s\n", version)
	fmt.Fprintf(&b, "auto_update: %s\n", autoUpdate)
	b.WriteString(importMetaEnd + "\n")
	return b.String()
}

// parseImportMetadata extracts the metadata key/value pairs from a commit
// message. ok is false when the message is not an import commit (no exact begin
// marker). A begin marker without a matching exact end marker is an error.
func parseImportMetadata(msg string) (fields map[string]string, ok bool, err error) {
	lines := strings.Split(msg, "\n")
	begin := -1
	for i, line := range lines {
		if line == importMetaBegin {
			begin = i
			break
		}
	}
	if begin < 0 {
		return nil, false, nil
	}

	end := -1
	for i := begin + 1; i < len(lines); i++ {
		if lines[i] == importMetaEnd {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, false, fmt.Errorf("import metadata begin marker without a matching %q end marker", importMetaEnd)
	}

	fields = make(map[string]string)
	for _, line := range lines[begin+1 : end] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, val, found := strings.Cut(line, ":")
		if !found {
			return nil, false, fmt.Errorf("malformed import metadata line %q", line)
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	return fields, true, nil
}

// findPreviousImport returns the most recent valid import commit for pkg
// reachable from HEAD, or nil when the package has never been imported. The
// scan matches the exact metadata begin marker in each commit touching
// pkgs/<pkg>, newest first; the first match is the previous import. A match
// that fails validateImportCommit is a hard error (corrupt lineage), not a
// reason to fall through to an older commit.
func findPreviousImport(root, pkg string) (*importCommit, error) {
	// An unborn branch (no commit yet) trivially has no prior import, and
	// git log would error on it.
	if exec.Command("git", "-C", root, "rev-parse", "--verify", "-q", "HEAD").Run() != nil {
		return nil, nil
	}

	pkgPath := path.Join("pkgs", pkg)
	out, err := exec.Command("git", "-C", root, "log", "--format=%H", "--", pkgPath).Output()
	if err != nil {
		return nil, fmt.Errorf("listing history for %s: %w", pkgPath, err)
	}

	for _, hash := range strings.Fields(string(out)) {
		msg, err := commitMessage(root, hash)
		if err != nil {
			return nil, err
		}
		fields, ok, err := parseImportMetadata(msg)
		if err != nil {
			return nil, fmt.Errorf("import commit %s: %w", hash, err)
		}
		if !ok {
			continue
		}
		if err := validateImportCommit(root, hash, pkg); err != nil {
			return nil, fmt.Errorf("import commit %s for %s is invalid: %w", hash, pkg, err)
		}
		return &importCommit{Hash: hash, Version: fields["version"]}, nil
	}
	return nil, nil
}

// validateImportCommit sanity-checks that commit is a well-formed import for
// pkg: its message carries both exact markers, and its tree contains nothing
// outside pkgs/<pkg>/. The structure inside pkgs/<pkg>/ is not checked.
func validateImportCommit(root, commit, pkg string) error {
	msg, err := commitMessage(root, commit)
	if err != nil {
		return err
	}
	if !hasExactLine(msg, importMetaBegin) {
		return fmt.Errorf("missing %q marker", importMetaBegin)
	}
	if !hasExactLine(msg, importMetaEnd) {
		return fmt.Errorf("missing %q marker", importMetaEnd)
	}

	out, err := exec.Command("git", "-C", root, "ls-tree", "-r", "--name-only", commit).Output()
	if err != nil {
		return fmt.Errorf("listing tree: %w", err)
	}
	prefix := path.Join("pkgs", pkg) + "/"
	for _, p := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, prefix) {
			return fmt.Errorf("tree contains %q outside %s", p, prefix)
		}
	}
	return nil
}

// hasExactLine reports whether msg contains line as a full line (not as a
// substring of a longer line).
func hasExactLine(msg, line string) bool {
	for _, l := range strings.Split(msg, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

// commitMessage returns the full commit message (subject + body) for a commit.
func commitMessage(root, commit string) (string, error) {
	out, err := exec.Command("git", "-C", root, "log", "-1", "--format=%B", commit).Output()
	if err != nil {
		return "", fmt.Errorf("reading commit %s: %w", commit, err)
	}
	return string(out), nil
}

// commitImportTree builds an import commit for pkg from the self-contained
// contentDir (which becomes the pkgs/<pkg>/ subtree) and returns its hash. The
// commit is constructed entirely through plumbing against a throwaway index, so
// neither the real index, the working tree, nor any branch is touched. parent
// is the previous import commit, or empty for a first (orphan) import.
func commitImportTree(root, contentDir, pkg, message, parent string) (string, error) {
	idx, err := os.CreateTemp("", "glbx-import-index-*")
	if err != nil {
		return "", fmt.Errorf("creating temp index: %w", err)
	}
	indexPath := idx.Name()
	idx.Close()
	// git refuses to read a zero-byte index file; let git create it fresh.
	os.Remove(indexPath)
	defer os.Remove(indexPath)

	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+indexPath)
		out, err := cmd.Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, exitErr.Stderr)
			}
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(out)), nil
	}

	// Stage contentDir into the throwaway index and capture it as a subtree.
	addCmd := exec.Command("git", "-C", root, "add", "-A")
	addCmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+indexPath, "GIT_WORK_TREE="+contentDir)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("staging import content: %w: %s", err, out)
	}
	subtree, err := git("write-tree")
	if err != nil {
		return "", err
	}

	// Re-read that subtree under pkgs/<pkg>/ into a fresh index, giving a
	// top-level tree whose only content is the package directory.
	if err := os.Remove(indexPath); err != nil {
		return "", fmt.Errorf("resetting temp index: %w", err)
	}
	if _, err := git("read-tree", "--prefix=pkgs/"+pkg+"/", subtree); err != nil {
		return "", err
	}
	topTree, err := git("write-tree")
	if err != nil {
		return "", err
	}

	args := []string{"commit-tree", topTree, "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return git(args...)
}
