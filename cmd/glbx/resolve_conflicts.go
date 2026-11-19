package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/provider"
)

// cmdResolveConflicts is the standalone entry point for the per-pkg_type
// auto-conflict-resolution strategy. The `import` command runs it inline on its
// own merge, but the CI update pipeline runs a `git merge` of its own (after a
// `--no-merge` import), so it needs to invoke the auto step by hand after a
// trial-merge conflict. This command does exactly the strategy and no more: it
// does NOT merge and does NOT commit — the caller owns the merge and its commit.
// It expects the working tree to be mid-merge with conflicts staged, regenerates
// and git-adds the files the pkg_type can resolve, prints the resulting status,
// and exits 0 only when no conflict remains.
func cmdResolveConflicts(args []string) error {
	fs := flag.NewFlagSet("resolve-conflicts", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: glbx resolve-conflicts [flags] <package>")
		fmt.Fprintln(os.Stderr, "\nAttempt the package's pkg_type auto-conflict-resolution strategy on a merge")
		fmt.Fprintln(os.Stderr, "already in progress in the working tree. For the kernel this regenerates the")
		fmt.Fprintln(os.Stderr, "generated debian/control and git-adds it; plain Debian packages have no strategy.")
		fmt.Fprintln(os.Stderr, "Does not merge and does not commit — the caller owns the merge commit.")
		fmt.Fprintln(os.Stderr, "\nExit status: 0 when no conflict remains; non-zero when conflicts remain or no")
		fmt.Fprintln(os.Stderr, "auto-resolution strategy is available for the package's pkg_type.")
		fmt.Fprintln(os.Stderr, "\nflags:")
		fs.PrintDefaults()
	}
	var params repeatedFlag
	fs.Var(&params, "params", "provider parameters as key=val,key=val (repeatable); see `glbx lockfile --params-help <namespace>`")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	outputDir := fs.String("output", ".", "working-tree root")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary (for container-backed strategies, e.g. the kernel)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		return fmt.Errorf("missing <package> argument")
	}
	pkg := fs.Arg(0)

	parsedParams, err := provider.ParseParams(params)
	if err != nil {
		return err
	}

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	ctx, l := rootContext(log.Lockfile)

	op := provider.OpContext{
		Ctx:       ctx,
		Store:     store,
		OutputDir: *outputDir,
		Params:    parsedParams,
		StubPath:  *stubPath,
	}

	resolver, err := provider.LookupConflictResolver(op, pkg)
	if err != nil {
		return err
	}
	if resolver == nil {
		return fmt.Errorf("no auto-resolution strategy is available for %s (its pkg_type defines none)", pkg)
	}

	resolved, err := resolver.ResolveConflicts(op, pkg)
	if err != nil {
		return fmt.Errorf("auto-resolve conflicts: %w", err)
	}

	status, serr := printGitStatus(*outputDir)
	if serr != nil {
		l.Error("could not read git status: %v", serr)
	} else {
		fmt.Fprint(os.Stderr, status)
	}

	if !resolved {
		return fmt.Errorf("auto-resolution handled its own files but other conflicts remain for %s; resolve them, then commit", pkg)
	}
	l.Info("auto-resolved conflicts for %s; staged and ready to commit", pkg)
	return nil
}
