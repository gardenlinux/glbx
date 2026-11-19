package provider

import (
	"fmt"
	"os/exec"
	"strings"
)

// hasRemainingConflicts reports whether the working tree at root still has any
// unmerged index entries — i.e. conflicts a resolution strategy has not cleared.
// It is the simple low-level check the auto-resolution path ends on: after a
// strategy regenerates and git-adds the files it owns, those paths leave the
// unmerged set, and anything still listed is a conflict the strategy could not
// handle. `git ls-files --unmerged` prints one or more stage lines per still-
// conflicted path and nothing at all for a conflict-free index.
func hasRemainingConflicts(root string) (bool, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "--unmerged").Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return false, fmt.Errorf("git ls-files --unmerged: %w: %s", err, exitErr.Stderr)
		}
		return false, fmt.Errorf("git ls-files --unmerged: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// gitAdd stages the given paths in the working tree at root, marking a resolved
// conflict so the path drops out of the unmerged set. Paths are relative to
// root.
func gitAdd(root string, paths ...string) error {
	args := append([]string{"-C", root, "add", "--"}, paths...)
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add %s: %w: %s", strings.Join(paths, " "), err, out)
	}
	return nil
}
