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

// openStore opens the object store at dir, or at the default root if empty. When
// GLBX_REGISTRY names a registry, a pull-through store is composed over the local
// one so a cold cache is filled from the registry on demand.
func openStore(dir string) (*objstore.Store, error) {
	local, err := objstore.NewLocal(dir)
	if err != nil {
		return nil, err
	}
	ref, insecure := registryFromEnv()
	if ref == "" {
		return local, nil
	}
	registry, err := objstore.NewRegistry(ref, insecure)
	if err != nil {
		return nil, err
	}
	return objstore.NewPullThrough(local, registry), nil
}

// resolveCacheDir resolves the object-store directory a command uses: dir, or
// the default root when dir is empty. Commands log this rather than asking the
// store for a path it was handed.
func resolveCacheDir(dir string) string {
	if dir == "" {
		return objstore.DefaultRoot()
	}
	return dir
}

// registryFromEnv reads the remote registry configuration from the environment:
// GLBX_REGISTRY is the "host[:port]/repo" reference (empty disables the remote),
// and GLBX_REGISTRY_INSECURE selects plaintext HTTP for a local test registry.
func registryFromEnv() (ref string, insecure bool) {
	ref = os.Getenv("GLBX_REGISTRY")
	insecure = os.Getenv("GLBX_REGISTRY_INSECURE") != ""
	return ref, insecure
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
