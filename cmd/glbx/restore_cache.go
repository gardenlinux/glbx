package main

import (
	"flag"
	"fmt"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/restore"
)

func cmdRestoreCache(args []string) error {
	fs := flag.NewFlagSet("restore-cache", flag.ExitOnError)
	cacheDir := fs.String("cache", "", "object-store cache directory")
	confDir := fs.String("conf-dir", "", "configuration directory")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	fs.Parse(args)

	confRoot := *confDir
	if confRoot == "" {
		confRoot = findConfDir()
	}
	if confRoot == "" {
		return fmt.Errorf("no conf-dir found from the current directory")
	}

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	ctx, _ := rootContext(log.Fetch)

	_, err = restore.Restore(restore.Config{
		Ctx:      ctx,
		Store:    store,
		ConfRoot: confRoot,
		Arch:     *arch,
	})
	return err
}
