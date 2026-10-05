package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/lockfile"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/resolver"
)

func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	cacheDir := fs.String("cache", "", "cache directory")
	indexHash := fs.String("index", "", "Packages index blob hash to resolve against")
	repo := fs.String("repo", "https://deb.debian.org/debian", "APT repository URL (used if --index not given)")
	dist := fs.String("dist", "testing", "distribution")
	arch := fs.String("arch", buildcfg.HostArch(), "architecture")
	cookie := fs.String("cookie", "", "InRelease cache cookie (only with --repo)")
	source := fs.Bool("source", false, "prefix each output line with <source>:")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: glbx resolve [--index <hash> | --repo <url>] [flags] <pkg1> [pkg2] ...\n\nResolves the given packages against a Packages index. Logs go to stderr; results to stdout.")
	}

	ctx, l := rootContextStderr(log.Deps)

	storeDir := *cacheDir
	if storeDir == "" {
		storeDir = objstore.DefaultRoot()
	}
	store, err := objstore.Open(storeDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	var idx *index.Index
	if *indexHash != "" {
		h, err := objstore.NewHash(*indexHash)
		if err != nil {
			return fmt.Errorf("parse index hash: %w", err)
		}
		r, err := store.Blobs.Open(h)
		if err != nil {
			return fmt.Errorf("open index blob: %w", err)
		}
		defer r.Close()
		idx, err = index.Load(r)
		if err != nil {
			return fmt.Errorf("load index: %w", err)
		}
	} else {
		idx, err = lockfile.FetchBinaryIndex(ctx, store, *repo, *dist, *arch, *cookie)
		if err != nil {
			return fmt.Errorf("fetch binary index: %w", err)
		}
	}

	var roots []resolver.Requirement
	for _, name := range fs.Args() {
		roots = append(roots, resolver.Requirement{
			Name:            name,
			VirtualEligible: true,
		})
	}

	l.Info("resolving %d package(s) against index (%d entries)", len(roots), idx.Len())

	res := resolver.New(idx, *arch)
	result, err := res.Resolve(roots)
	if err != nil {
		return fmt.Errorf("resolve failed:\n%v", err)
	}

	l.Info("resolved %d packages", len(result.Packages))
	for _, pkg := range result.Packages {
		if *source {
			fmt.Fprintf(os.Stdout, "%s:%s %s\n", pkg.SourceName(), pkg.Name, pkg.Version)
		} else {
			fmt.Fprintf(os.Stdout, "%s %s\n", pkg.Name, pkg.Version)
		}
	}

	return nil
}
