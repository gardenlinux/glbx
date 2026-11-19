package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
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

	return runMergeWithAutoResolve(ctx, l, store, *outputDir, parsedParams, target.Namespace, result)
}

// runMergeWithAutoResolve merges the import commit into the current branch as a
// subprocess (not syscall.Exec, so the process survives to act on the outcome).
// A clean merge is left as git made it. A conflicting merge triggers an optional
// per-pkg_type auto-resolution attempt: if the package's build.yml selects a
// strategy and it clears every conflict, the merge is finalized with a commit
// and the import succeeds; otherwise the partial resolution is left staged, the
// working-tree status is printed, and a non-zero result is returned so the
// exit-status contract (0 only when the tree is conflict-free) is preserved.
func runMergeWithAutoResolve(ctx context.Context, l *log.Logger, store *objstore.Store, outputDir string, params provider.Params, namespace string, result *provider.ImportResult) error {
	argv := []string{"-C", outputDir, "merge", "--no-edit"}
	if result.FirstImport {
		argv = append(argv, "--allow-unrelated-histories")
	}
	argv = append(argv, result.CommitHash)

	merge := exec.Command("git", argv...)
	merge.Stdin = os.Stdin
	merge.Stdout = os.Stdout
	merge.Stderr = os.Stderr
	err := merge.Run()
	if err == nil {
		// Clean merge: git already produced the merge commit.
		return nil
	}
	if _, ok := err.(*exec.ExitError); !ok {
		return fmt.Errorf("git merge: %w", err)
	}

	// The merge stopped with conflicts. Attempt an auto-resolution if this
	// package's pkg_type defines one; if it does not, the conflict stands exactly
	// as it does today.
	op := provider.OpContext{Ctx: ctx, Store: store, OutputDir: outputDir, Params: params}
	resolver, lerr := provider.LookupConflictResolver(op, result.Name)
	if lerr != nil {
		return lerr
	}
	if resolver == nil {
		return fmt.Errorf("merge of %s left unresolved conflicts and no auto-resolution is available for this package; resolve them, then commit", result.Name)
	}

	l.Info("merge conflicted; attempting %s auto-resolution for %s…", namespace, result.Name)
	resolved, rerr := resolver.ResolveConflicts(op, result.Name)
	if rerr != nil {
		return fmt.Errorf("auto-resolve conflicts: %w", rerr)
	}
	if !resolved {
		if status, serr := printGitStatus(outputDir); serr != nil {
			l.Error("could not read git status: %v", serr)
		} else {
			fmt.Fprint(os.Stderr, status)
		}
		return fmt.Errorf("auto-resolution handled its own files but other conflicts remain; resolve them (see status above), then commit")
	}

	// Every conflict is resolved and staged: finalize the merge commit, the same
	// way git would have for a clean merge.
	commit := exec.Command("git", "-C", outputDir, "commit", "--no-edit")
	commit.Stdin = os.Stdin
	commit.Stdout = os.Stdout
	commit.Stderr = os.Stderr
	if cerr := commit.Run(); cerr != nil {
		return fmt.Errorf("commit auto-resolved merge: %w", cerr)
	}
	l.Info("auto-resolved merge conflicts for %s and committed the merge", result.Name)
	return nil
}

// printGitStatus returns the short-format working-tree status, for surfacing
// what remains after a partial auto-resolution.
func printGitStatus(outputDir string) (string, error) {
	out, err := exec.Command("git", "-C", outputDir, "status", "--short").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
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
