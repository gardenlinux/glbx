package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gardenlinux/glbx/internal/build"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

func cmdCache(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: glbx cache <status|gc|blobs|map>")
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "status":
		return cacheStatus(subArgs)
	case "gc":
		return cacheGC(subArgs)
	case "blobs":
		return cacheBlobs(subArgs)
	case "map":
		return cacheMap(subArgs)
	default:
		return fmt.Errorf("unknown cache subcommand %q", sub)
	}
}

func cacheStatus(args []string) error {
	store, err := openStore("")
	if err != nil {
		return err
	}

	var blobCount, mapCount int
	store.Blobs.Iterate(func(objstore.Hash) error { blobCount++; return nil })
	store.Map.Iterate(func(objstore.Hash) error { mapCount++; return nil })

	_, l := rootContext(log.Engine)
	l.Info("cache: %s", store.Root())
	l.Info("blobs: %d, map entries: %d", blobCount, mapCount)
	return nil
}

// cacheGC computes the keep-set from the current checkout's build graph —
// every built, reachable artifact's manifest and leaf blobs — then reclaims
// everything else. The store is a pure cache: a reclaimed output rebuilds and a
// reclaimed input re-fetches, so graph reachability is the entire policy.
func cacheGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "show what would be deleted")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture for the build graph")
	confDir := fs.String("conf-dir", "", "configuration directory (contains pkgs/, rootfs.yml)")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	fs.Parse(args)

	store, err := openStore("")
	if err != nil {
		return err
	}
	_, l := rootContext(log.Engine)

	keep := make(map[objstore.Hash]struct{})

	confRoot := *confDir
	if confRoot == "" {
		confRoot = findConfDir()
	}
	if confRoot == "" {
		l.Warn("no conf-dir found; GC keep-set is empty (all build outputs collectible)")
	} else {
		graphResult, err := build.BuildGraph(build.GraphConfig{
			ConfDir:  confRoot,
			Arch:     *arch,
			Store:    store,
			StubPath: *stubPath,
		})
		if err != nil {
			return fmt.Errorf("build graph: %w", err)
		}
		reachable := 0
		for _, key := range graphResult.Graph.Keys() {
			a := graphResult.Graph.Find(key)
			manifest, leaves, err := a.OutputRefs(store)
			if err != nil {
				continue // not built — contributes nothing
			}
			keep[manifest] = struct{}{}
			for _, h := range leaves {
				keep[h] = struct{}{}
			}
			reachable++
		}
		l.Info("graph: %d nodes, %d built and reachable", graphResult.Graph.Len(), reachable)
	}
	l.Info("keep-set: %d blobs", len(keep))

	if *dryRun {
		var wouldDelete int
		store.Blobs.Iterate(func(h objstore.Hash) error {
			if _, ok := keep[h]; !ok {
				wouldDelete++
			}
			return nil
		})
		l.Info("would delete %d unreachable blobs", wouldDelete)
		return nil
	}

	blobs, entries, err := store.GC(keep)
	if err != nil {
		return fmt.Errorf("gc: %w", err)
	}
	l.Info("deleted %d blobs, %d map entries", blobs, entries)
	return nil
}

func cacheBlobs(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: glbx cache blobs <get|store|list|delete|check|path>")
	}
	store, err := openStore("")
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		return store.Blobs.Iterate(func(h objstore.Hash) error {
			fmt.Println(h)
			return nil
		})
	case "check":
		h, err := blobArg(args)
		if err != nil {
			return err
		}
		if store.Blobs.Has(h) {
			fmt.Println("exists")
		} else {
			fmt.Println("not found")
		}
		return nil
	case "path":
		h, err := blobArg(args)
		if err != nil {
			return err
		}
		fmt.Println(store.Blobs.Path(h))
		return nil
	case "store":
		if len(args) < 2 {
			return fmt.Errorf("usage: glbx cache blobs store <file>")
		}
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		hash, err := store.Blobs.Store(f)
		if err != nil {
			return err
		}
		fmt.Println(hash)
		return nil
	case "get":
		h, err := blobArg(args)
		if err != nil {
			return err
		}
		r, err := store.Blobs.Open(h)
		if err != nil {
			return err
		}
		defer r.Close()
		if _, err := io.Copy(os.Stdout, r); err != nil {
			return fmt.Errorf("write blob to stdout: %w", err)
		}
		return nil
	case "delete":
		h, err := blobArg(args)
		if err != nil {
			return err
		}
		return store.Blobs.Delete(h)
	default:
		return fmt.Errorf("unknown blobs subcommand %q", args[0])
	}
}

func blobArg(args []string) (objstore.Hash, error) {
	if len(args) < 2 {
		return objstore.Hash{}, fmt.Errorf("usage: glbx cache blobs %s <hash>", args[0])
	}
	return objstore.NewHash(args[1])
}

func cacheMap(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: glbx cache map <get|set|list|delete|check>")
	}
	store, err := openStore("")
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		return store.Map.Iterate(func(k objstore.Hash) error {
			v, _ := store.Map.Get(k)
			fmt.Printf("%s -> %s\n", k, v)
			return nil
		})
	case "get":
		if len(args) < 2 {
			return fmt.Errorf("usage: glbx cache map get <key>")
		}
		k, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		v, err := store.Map.Get(k)
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: glbx cache map set <key> <value>")
		}
		k, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		v, err := objstore.NewHash(args[2])
		if err != nil {
			return err
		}
		return store.Map.Set(k, v, false)
	case "check":
		if len(args) < 2 {
			return fmt.Errorf("usage: glbx cache map check <key>")
		}
		k, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		if store.Map.Has(k) {
			fmt.Println("exists")
		} else {
			fmt.Println("not found")
		}
		return nil
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: glbx cache map delete <key>")
		}
		k, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		return store.Map.Delete(k)
	default:
		return fmt.Errorf("unknown map subcommand %q", args[0])
	}
}
