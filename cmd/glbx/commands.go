package main

import (
	"flag"
	"fmt"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/debian/aptrepo"
	"github.com/gardenlinux/glbx/internal/lockfile"
	"github.com/gardenlinux/glbx/internal/log"
)

func cmdLockfile(args []string) error {
	fs := flag.NewFlagSet("lockfile", flag.ExitOnError)
	repo := fs.String("repo", "https://deb.debian.org/debian", "APT repository URL")
	snapshot := fs.String("snapshot", aptrepo.DefaultSnapshotBase, "snapshot archive file endpoint (SHA1-addressed), recorded as a secondary retrieval URL")
	dist := fs.String("dist", "testing", "distribution")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	outputDir := fs.String("output", ".", "working-tree root")
	cookie := fs.String("cookie", "", "InRelease cache cookie (reuse a cached InRelease within a session)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: glbx lockfile [flags] <package-name>")
	}

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	ctx, l := rootContext(log.Lockfile)

	result, err := lockfile.Generate(lockfile.Config{
		Ctx:          ctx,
		Store:        store,
		RepoURL:      *repo,
		SnapshotBase: *snapshot,
		Dist:         *dist,
		Arch:         *arch,
		OutputDir:    *outputDir,
		PkgName:      fs.Arg(0),
		Cookie:       *cookie,
	})
	if err != nil {
		return err
	}

	l.Info("generated %s (%s): %d packages", result.Path, result.Arch, result.Packages)
	return nil
}

func cmdLockfileRootfs(args []string) error {
	fs := flag.NewFlagSet("lockfile-rootfs", flag.ExitOnError)
	repo := fs.String("repo", "https://deb.debian.org/debian", "APT repository URL")
	snapshot := fs.String("snapshot", aptrepo.DefaultSnapshotBase, "snapshot archive file endpoint (SHA1-addressed), recorded as a secondary retrieval URL")
	dist := fs.String("dist", "testing", "distribution")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	outputDir := fs.String("output", ".", "working-tree root")
	cookie := fs.String("cookie", "", "InRelease cache cookie (reuse a cached InRelease within a session)")
	fs.Parse(args)

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	ctx, l := rootContext(log.Lockfile)

	result, err := lockfile.GenerateRootfs(lockfile.RootfsConfig{
		Ctx:          ctx,
		Store:        store,
		RepoURL:      *repo,
		SnapshotBase: *snapshot,
		Dist:         *dist,
		Arch:         *arch,
		OutputDir:    *outputDir,
		Cookie:       *cookie,
	})
	if err != nil {
		return err
	}

	l.Info("generated %s (%s): %d packages", result.Path, result.Arch, result.Packages)
	return nil
}

func cmdStatus(args []string) error {
	_, l := rootContext(log.Engine)
	store, err := openStore("")
	if err != nil {
		return err
	}
	l.Info("cache: %s", store.Root())
	if confRoot := findConfDir(); confRoot != "" {
		l.Info("working tree: %s", confRoot)
	} else {
		l.Info("working tree: not found from the current directory")
	}
	return nil
}
