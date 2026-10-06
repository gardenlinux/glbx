// Package restore populates the object-store cache from the pins recorded in a
// working tree. For every source archive and build-tooling .deb pinned under the
// tree, it ensures the blob is present locally, downloading it by its recorded
// URLs when it is not. Each pin lists its URLs in preference order; restore tries
// them in turn and keeps the first whose bytes match the pinned hash.
package restore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// Config parameterizes a cache restore.
type Config struct {
	Ctx         context.Context
	Store       *objstore.Store
	ConfRoot    string
	Arch        string
	HTTPClient  *http.Client
	Concurrency int
}

// Result summarizes a restore run.
type Result struct {
	Restored int // blobs downloaded into the cache
	Cached   int // blobs already present
	Failed   int // blobs no URL could supply
}

func (cfg *Config) applyDefaults() {
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	if cfg.Arch == "" {
		cfg.Arch = buildcfg.HostArch()
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 16
	}
}

// item is one blob to restore: its expected content hash, the URLs it may be
// fetched from in preference order, and a human label for diagnostics.
type item struct {
	hash  objstore.Hash
	urls  []string
	label string
}

// Restore ensures every pinned blob in the working tree is present in the store,
// downloading absent ones from their recorded URLs. It returns a per-blob error
// (joined across every blob that no URL could supply) when any blob is missing.
func Restore(cfg Config) (Result, error) {
	cfg.applyDefaults()
	l := log.From(cfg.Ctx, log.Fetch)

	if cfg.Store == nil {
		return Result{}, fmt.Errorf("restore: Store is required")
	}
	if cfg.ConfRoot == "" {
		return Result{}, fmt.Errorf("restore: ConfRoot is required")
	}

	items, err := collectItems(cfg.ConfRoot, cfg.Arch)
	if err != nil {
		return Result{}, err
	}
	l.Info("restoring %d pinned blobs (arch %s)", len(items), cfg.Arch)

	var restored, cached, failed atomic.Int32
	errs := make([]error, len(items))

	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.Concurrency)
	for i := range items {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			fetched, err := fetchItem(cfg, items[i])
			switch {
			case err != nil:
				failed.Add(1)
				errs[i] = err
			case fetched:
				restored.Add(1)
			default:
				cached.Add(1)
			}
		}(i)
	}
	wg.Wait()

	res := Result{Restored: int(restored.Load()), Cached: int(cached.Load()), Failed: int(failed.Load())}
	l.Info("restore complete: %d restored, %d cached, %d failed", res.Restored, res.Cached, res.Failed)

	return res, errors.Join(errs...)
}

// fetchItem ensures a single blob is cached. It first lets the store satisfy the
// hash (a local hit, or a pull-through once a remote is configured); on a miss it
// downloads from the item's URLs in order, keeping the first whose bytes match.
// It reports whether a download happened, and an error only when every URL fails.
func fetchItem(cfg Config, it item) (bool, error) {
	l := log.From(cfg.Ctx, log.Fetch)

	if err := cfg.Store.EnsureBlob(it.hash); err == nil && cfg.Store.Blobs.Has(it.hash) {
		l.Debug("cached %s (%s)", it.label, it.hash.Short())
		return false, nil
	}

	var urlErrs []error
	for _, u := range it.urls {
		if err := download(cfg.HTTPClient, u, cfg.Store, it.hash); err != nil {
			urlErrs = append(urlErrs, fmt.Errorf("%s: %w", u, err))
			continue
		}
		l.Info("restored %s (%s) from %s", it.label, it.hash.Short(), u)
		return true, nil
	}

	if len(it.urls) == 0 {
		return false, fmt.Errorf("%s (%s): no URLs recorded", it.label, it.hash.Short())
	}
	return false, fmt.Errorf("%s (%s): %w", it.label, it.hash.Short(), errors.Join(urlErrs...))
}

// download fetches url and streams it into the store, which hashes the content as
// it writes and returns the resulting digest. The download succeeds only when
// that digest equals want. A blob written under a mismatching digest is left in
// place for garbage collection; it is never the hash a build looks up.
func download(client *http.Client, url string, store *objstore.Store, want objstore.Hash) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	got, err := store.Blobs.Store(resp.Body)
	if err != nil {
		return err
	}
	if !got.Equal(want) {
		return fmt.Errorf("hash mismatch: got %s, want %s", got, want)
	}
	return nil
}

// collectItems reads every pin in the working tree into a deduplicated restore
// set: each package's source archives and arch-matching build-tooling .debs, plus
// the image configuration tooling from the root rootfs-deps.yml.
func collectItems(confRoot, arch string) ([]item, error) {
	seen := make(map[string]struct{})
	var items []item
	add := func(h objstore.Hash, urls []string, label string) {
		if _, ok := seen[h.String()]; ok {
			return
		}
		seen[h.String()] = struct{}{}
		items = append(items, item{hash: h, urls: urls, label: label})
	}

	pkgsDir := filepath.Join(confRoot, "pkgs")
	entries, err := os.ReadDir(pkgsDir)
	if err != nil {
		return nil, fmt.Errorf("restore: reading %s: %w", pkgsDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pkgDir := filepath.Join(pkgsDir, e.Name())

		sources, err := buildcfg.LoadSourcesYML(pkgDir)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("restore: %s/sources.yml: %w", e.Name(), err)
		}
		for _, s := range sources {
			add(s.Hash, s.URLs, fmt.Sprintf("%s source %s", e.Name(), s.File))
		}

		depsPath := filepath.Join(pkgDir, "build-deps.yml")
		if deps, err := buildcfg.LoadBuildDeps(depsPath); err == nil {
			for _, f := range buildcfg.FilesForArch(deps, arch) {
				add(f.Hash, f.URLs, fmt.Sprintf("%s build-dep", e.Name()))
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("restore: %s/build-deps.yml: %w", e.Name(), err)
		}
	}

	rootfsPath := filepath.Join(confRoot, "rootfs-deps.yml")
	if deps, err := buildcfg.LoadBuildDeps(rootfsPath); err == nil {
		for _, f := range buildcfg.FilesForArch(deps, arch) {
			add(f.Hash, f.URLs, "rootfs-dep")
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("restore: rootfs-deps.yml: %w", err)
	}

	return items, nil
}
