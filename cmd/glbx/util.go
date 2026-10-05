package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// findConfDir walks up from the working directory to the working-tree root,
// identified by a pkgs/ directory alongside a rootfs.yml.
func findConfDir() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "pkgs")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "rootfs.yml")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// openStore opens the object store at dir, or at the default root if empty.
func openStore(dir string) (*objstore.Store, error) {
	if dir == "" {
		dir = objstore.DefaultRoot()
	}
	return objstore.Open(dir)
}

// rootContext returns a context carrying a console log target and a logger for
// the given component.
func rootContext(c log.Component) (context.Context, *log.Logger) {
	ctx := log.WithTarget(context.Background(), log.NewConsoleTarget())
	return ctx, log.From(ctx, c)
}

// rootContextStderr is like rootContext but routes every level to stderr, for
// commands whose stdout carries machine-parseable output (e.g. resolve).
func rootContextStderr(c log.Component) (context.Context, *log.Logger) {
	ctx := log.WithTarget(context.Background(), log.NewStderrConsoleTarget())
	return ctx, log.From(ctx, c)
}
