package importer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit creates a throwaway git repository with a committed base file and
// deterministic identity, returning its root.
func gitInit(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.invalid")
	runGit(t, root, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-qm", "base")
	return root
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// writeContent lays out a self-contained pkgs/<name> content tree under a fresh
// temp dir and returns its path.
func writeContent(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestImportMetadataRoundTrip(t *testing.T) {
	msg := formatImportMessage("glibc", "2.40-3", "debian:testing")

	if !strings.HasPrefix(msg, "import glibc 2.40-3 from debian\n") {
		t.Errorf("unexpected subject line in %q", msg)
	}

	fields, ok, err := parseImportMetadata(msg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ok {
		t.Fatal("expected metadata to be recognized")
	}
	for k, want := range map[string]string{"pkg": "glibc", "version": "2.40-3", "auto_update": "debian:testing"} {
		if got := fields[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestParseImportMetadataExactMarkers(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		wantOK  bool
		wantErr bool
	}{
		{"no markers", "just a normal commit\n", false, false},
		{"wrong dash count", "subj\n\n--BEGIN PKG IMPORT METADATA--\npkg: x\n--END PKG IMPORT METADATA--\n", false, false},
		{"trailing space on begin", "subj\n\n" + importMetaBegin + " \npkg: x\n" + importMetaEnd + "\n", false, false},
		{"begin without end", "subj\n\n" + importMetaBegin + "\npkg: x\n", false, true},
		{"wrong end spacing", "subj\n\n" + importMetaBegin + "\npkg: x\n---END PKG IMPORT METADATA---\n", false, true},
		{"valid", "subj\n\n" + importMetaBegin + "\npkg: x\n" + importMetaEnd + "\n", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := parseImportMetadata(tc.msg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}

func TestCommitImportTreeDetached(t *testing.T) {
	root := gitInit(t)
	headBefore := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	content := writeContent(t, map[string]string{
		"debian/control": "Source: foo\n",
		"sources.yml":    "sources: []\n",
	})
	msg := formatImportMessage("foo", "1.0", "debian:testing")
	commit, err := commitImportTree(root, content, "foo", msg, "")
	if err != nil {
		t.Fatalf("commitImportTree: %v", err)
	}

	// HEAD, the index, and the working tree must be untouched.
	if headAfter := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Errorf("HEAD moved: %s -> %s", headBefore, headAfter)
	}
	if status := runGit(t, root, "status", "--porcelain"); status != "" {
		t.Errorf("working tree/index dirtied: %q", status)
	}
	if _, err := os.Stat(filepath.Join(root, "pkgs")); !os.IsNotExist(err) {
		t.Errorf("pkgs/ should not exist in the working tree yet")
	}

	// The commit's tree must contain only pkgs/foo content and have no parent.
	tree := runGit(t, root, "ls-tree", "-r", "--name-only", commit)
	for _, p := range strings.Fields(tree) {
		if !strings.HasPrefix(p, "pkgs/foo/") {
			t.Errorf("unexpected path in import tree: %q", p)
		}
	}
	if parents := strings.TrimSpace(runGit(t, root, "rev-list", "--parents", "-n", "1", commit)); parents != commit {
		t.Errorf("first import should be an orphan, got parents line %q", parents)
	}
}

func TestFindPreviousImport(t *testing.T) {
	root := gitInit(t)

	// First import (orphan) merged in.
	c1content := writeContent(t, map[string]string{"debian/control": "Source: foo\nv1\n"})
	c1, err := commitImportTree(root, c1content, "foo", formatImportMessage("foo", "1.0", "debian:testing"), "")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "merge", "--no-edit", "--allow-unrelated-histories", c1)

	// A manual maintainer edit — the last commit touching pkgs/foo, but NOT an
	// import. findPreviousImport must skip it and find the real import.
	if err := os.WriteFile(filepath.Join(root, "pkgs/foo/debian/control"), []byte("Source: foo\nv1\nLOCAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "commit", "-aqm", "local maintainer tweak")

	prev, err := findPreviousImport(root, "foo")
	if err != nil {
		t.Fatalf("findPreviousImport: %v", err)
	}
	if prev == nil {
		t.Fatal("expected to find the first import")
	}
	if prev.Hash != c1 {
		t.Errorf("found %s, want import commit %s", prev.Hash, c1)
	}
	if prev.Version != "1.0" {
		t.Errorf("version = %q, want 1.0", prev.Version)
	}

	// A package never imported returns nil, no error.
	none, err := findPreviousImport(root, "bar")
	if err != nil {
		t.Fatalf("findPreviousImport(bar): %v", err)
	}
	if none != nil {
		t.Errorf("expected nil for never-imported package, got %+v", none)
	}
}

func TestFindPreviousImportRejectsCorruptLineage(t *testing.T) {
	root := gitInit(t)

	// Craft a commit that carries the import markers but whose tree escapes
	// pkgs/foo (a file at the repo root). This is corrupt lineage: it must be a
	// hard error, not silently skipped.
	bad := writeContent(t, map[string]string{
		"debian/control": "Source: foo\n",
	})
	// Put a stray top-level file alongside pkgs/foo in the import tree by
	// committing a tree that mixes prefixes.
	idx := filepath.Join(t.TempDir(), "idx")
	strayWork := writeContent(t, map[string]string{
		"pkgs/foo/debian/control": "Source: foo\n",
		"evil.txt":                "outside\n",
	})
	addCmd := exec.Command("git", "-C", root, "add", "-A")
	addCmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+idx, "GIT_WORK_TREE="+strayWork)
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("stage stray tree: %v\n%s", err, out)
	}
	writeTree := exec.Command("git", "-C", root, "write-tree")
	writeTree.Env = append(os.Environ(), "GIT_INDEX_FILE="+idx)
	treeOut, err := writeTree.Output()
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(string(treeOut))
	commitTree := exec.Command("git", "-C", root, "commit-tree", tree, "-m", formatImportMessage("foo", "9.9", "debian:testing"))
	commitOut, err := commitTree.Output()
	if err != nil {
		t.Fatal(err)
	}
	badCommit := strings.TrimSpace(string(commitOut))
	runGit(t, root, "merge", "--no-edit", "--allow-unrelated-histories", badCommit)
	_ = bad

	if _, err := findPreviousImport(root, "foo"); err == nil {
		t.Fatal("expected a hard error on corrupt import lineage, got nil")
	}
}

func TestIsGitRepo(t *testing.T) {
	if isGitRepo(t.TempDir()) {
		t.Error("empty temp dir should not be a git repo")
	}
	if !isGitRepo(gitInit(t)) {
		t.Error("initialized repo should be recognized")
	}
}
