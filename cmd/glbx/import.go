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

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/provider"
)

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: glbx import [flags] <namespace>:<package>")
		fmt.Fprintln(os.Stderr, "\nImport an upstream source package into the working tree. The namespace is")
		fmt.Fprintln(os.Stderr, "always explicit (e.g. debian:linux): import creates pkgs/<name>/, so there")
		fmt.Fprintln(os.Stderr, "is no build.yml to dispatch on. By default the import is recorded as a commit")
		fmt.Fprintln(os.Stderr, "on the package's upstream lineage and merged into the current branch.")
		fmt.Fprintln(os.Stderr, "\nflags:")
		fs.PrintDefaults()
	}
	var params repeatedFlag
	fs.Var(&params, "params", "provider parameters as key=val,key=val (repeatable); see --params-help <namespace>")
	paramsHelp := fs.String("params-help", "", "print the parameters a namespace accepts and exit")
	updateTag := fs.String("update-tag", "", "auto-update source recorded in the import commit (default \"debian:<dist>\")")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	outputDir := fs.String("output", ".", "working-tree root to import into")
	noGitHistory := fs.Bool("no-git-history", false, "write pkgs/<package>/ directly instead of recording an import commit")
	noMerge := fs.Bool("no-merge", false, "build the import commit and print its result as JSON on stdout, without merging")
	fs.Parse(args)

	if *paramsHelp != "" {
		imp, err := provider.LookupImporter(*paramsHelp)
		if err != nil {
			return err
		}
		fmt.Println(imp.ParamsHelp())
		return nil
	}

	if fs.NArg() < 1 {
		fs.Usage()
		return fmt.Errorf("missing <namespace>:<package> argument")
	}
	target, err := provider.ParseTarget(fs.Arg(0))
	if err != nil {
		return err
	}
	if target.Arg == "" {
		return fmt.Errorf("import target %q must name a package (e.g. debian:%s)", fs.Arg(0), target.Namespace)
	}
	parsedParams, err := provider.ParseParams(params)
	if err != nil {
		return err
	}
	imp, err := provider.LookupImporter(target.Namespace)
	if err != nil {
		return err
	}

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

	result, err := imp.Import(provider.OpContext{
		Ctx:       ctx,
		Store:     store,
		OutputDir: *outputDir,
		Params:    parsedParams,
	}, target.Arg, provider.ImportOptions{
		GitHistory: !*noGitHistory,
		Merge:      !*noMerge,
		UpdateTag:  *updateTag,
	})
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
func printImportResult(w io.Writer, result *provider.ImportResult) error {
	obj := importJSON{
		Pkg:            result.Name,
		Version:        result.Version,
		Commit:         result.CommitHash,
		FirstImport:    result.FirstImport,
		AlreadyPresent: result.AlreadyPresent,
	}
	return json.NewEncoder(w).Encode(obj)
}
