package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/gardenlinux/glbx/internal/debian/aptrepo"
	"github.com/gardenlinux/glbx/internal/importer"
	"github.com/gardenlinux/glbx/internal/log"
)

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: glbx import [flags] <package>")
		fmt.Fprintln(os.Stderr, "\nImport a Debian source package. <package> is the source package name.")
		fmt.Fprintln(os.Stderr, "By default the import is recorded as a commit on the package's upstream")
		fmt.Fprintln(os.Stderr, "lineage and merged into the current branch.")
		fmt.Fprintln(os.Stderr, "\nflags:")
		fs.PrintDefaults()
	}
	repo := fs.String("repo", "https://deb.debian.org/debian", "APT repository URL")
	snapshot := fs.String("snapshot", aptrepo.DefaultSnapshotBase, "snapshot archive file endpoint (SHA1-addressed), recorded as a secondary retrieval URL")
	dist := fs.String("dist", "testing", "distribution")
	keyring := fs.String("keyring", "/usr/share/keyrings/debian-archive-keyring.gpg", "GPG keyring path")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	outputDir := fs.String("output", ".", "working-tree root to import into")
	noVerify := fs.Bool("no-verify", false, "skip GPG signature verification")
	noGitHistory := fs.Bool("no-git-history", false, "write pkgs/<package>/ directly instead of recording an import commit")
	cookie := fs.String("cookie", "", "InRelease cache cookie (reuse a cached InRelease within a session)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		return fmt.Errorf("missing <package> argument")
	}
	pkgName := fs.Arg(0)

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	ctx, l := rootContext(log.Importer)

	result, err := importer.Import(importer.ImportConfig{
		Ctx:          ctx,
		Store:        store,
		RepoURL:      *repo,
		SnapshotBase: *snapshot,
		Dist:         *dist,
		Keyring:      *keyring,
		OutputDir:    *outputDir,
		NoVerify:     *noVerify,
		GitHistory:   !*noGitHistory,
		Cookie:       *cookie,
	}, pkgName)
	if err != nil {
		return err
	}

	if !result.Committed {
		// Direct-write or an already-present no-op: the importer logged the
		// outcome; nothing more to do.
		if !result.AlreadyPresent {
			l.Info("imported %s %s (format: %s)", result.Name, result.Version, result.Format)
			for _, s := range result.Sources {
				l.Info("orig: %s (%s)", s.Name, s.Hash)
			}
		}
		return nil
	}

	short := result.CommitHash
	if len(short) > 12 {
		short = short[:12]
	}
	l.Info("imported %s %s as %s; git merging…", result.Name, result.Version, short)

	// Replace this process with git merge so its exit status and output
	// (including an unresolved conflict) propagate verbatim.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("git not found: %w", err)
	}
	argv := []string{"git", "-C", *outputDir, "merge", "--no-edit"}
	if result.FirstImport {
		argv = append(argv, "--allow-unrelated-histories")
	}
	argv = append(argv, result.CommitHash)
	return syscall.Exec(gitPath, argv, os.Environ())
}
