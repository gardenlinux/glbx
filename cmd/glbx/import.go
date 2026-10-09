package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	updateTag := fs.String("update-tag", "", "auto-update source recorded in the import commit (default \"debian:<dist>\")")
	keyring := fs.String("keyring", "/usr/share/keyrings/debian-archive-keyring.gpg", "GPG keyring path")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	outputDir := fs.String("output", ".", "working-tree root to import into")
	noVerify := fs.Bool("no-verify", false, "skip GPG signature verification")
	noGitHistory := fs.Bool("no-git-history", false, "write pkgs/<package>/ directly instead of recording an import commit")
	noMerge := fs.Bool("no-merge", false, "build the import commit and print its result as JSON on stdout, without merging")
	cookie := fs.String("cookie", "", "InRelease cache cookie (reuse a cached InRelease within a session)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		return fmt.Errorf("missing <package> argument")
	}
	pkgName := fs.Arg(0)

	if *noMerge && *noGitHistory {
		return fmt.Errorf("--no-merge requires git-history mode (it reports the import commit to merge); --no-git-history writes no commit")
	}

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	// In --no-merge mode stdout carries the machine-readable result, so route
	// logs to stderr; otherwise logs go to the console as usual.
	var ctx context.Context
	var l *log.Logger
	if *noMerge {
		ctx, l = rootContextStderr(log.Importer)
	} else {
		ctx, l = rootContext(log.Importer)
	}

	result, err := importer.Import(importer.ImportConfig{
		Ctx:          ctx,
		Store:        store,
		RepoURL:      *repo,
		SnapshotBase: *snapshot,
		Dist:         *dist,
		UpdateTag:    *updateTag,
		Keyring:      *keyring,
		OutputDir:    *outputDir,
		NoVerify:     *noVerify,
		GitHistory:   !*noGitHistory,
		Cookie:       *cookie,
	}, pkgName)
	if err != nil {
		return err
	}

	if *noMerge {
		return printImportResult(os.Stdout, result)
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

// importJSON is the machine-readable result printed on stdout by import
// --no-merge: one object a driver reads to decide what to do next. An empty
// commit with already_present true means a prior import already pins this
// version and there is nothing to merge.
type importJSON struct {
	Pkg            string `json:"pkg"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	FirstImport    bool   `json:"first_import"`
	AlreadyPresent bool   `json:"already_present"`
}

// printImportResult writes the import result as a single JSON object for
// --no-merge mode.
func printImportResult(w io.Writer, result *importer.ImportResult) error {
	obj := importJSON{
		Pkg:            result.Name,
		Version:        result.Version,
		Commit:         result.CommitHash,
		FirstImport:    result.FirstImport,
		AlreadyPresent: result.AlreadyPresent,
	}
	return json.NewEncoder(w).Encode(obj)
}
