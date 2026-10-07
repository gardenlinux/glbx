package main

import (
	"flag"
	"fmt"

	"github.com/gardenlinux/glbx/internal/build"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/restore"
)

// cmdPublish mirrors a checkout's locally-held content to an OCI registry. It
// reads from the local store and writes to the registry, uploading only the
// blobs and aliases the registry lacks. The worklist is the union of the two
// enumerations the system already defines: the recorded inputs restore collects
// and the built outputs garbage collection walks. With --target it is scoped to
// a single built node's manifest, output blobs, and identity alias.
func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	confDir := fs.String("conf-dir", "", "configuration directory (contains pkgs/, rootfs.yml)")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	registry := fs.String("registry", "", "target registry reference host[:port]/repo (default $GLBX_REGISTRY)")
	target := fs.String("target", "", "publish only the built node with this Key (e.g. rootfs:amd64)")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	fs.Parse(args)

	ref := *registry
	insecure := false
	if ref == "" {
		ref, insecure = registryFromEnv()
	} else {
		_, insecure = registryFromEnv()
	}
	if ref == "" {
		return fmt.Errorf("no registry given; pass --registry or set GLBX_REGISTRY")
	}

	confRoot := *confDir
	if confRoot == "" {
		confRoot = findConfDir()
	}
	if confRoot == "" {
		return fmt.Errorf("cannot find configuration directory; use --conf-dir")
	}

	// Publishing reads the local store directly — never through the registry it
	// is filling — so open a pure-local store regardless of GLBX_REGISTRY.
	storeDir := *cacheDir
	if storeDir == "" {
		storeDir = objstore.DefaultRoot()
	}
	store, err := objstore.NewLocal(storeDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	_, l := rootContext(log.Engine)

	graphResult, err := build.BuildGraph(build.GraphConfig{
		ConfDir:  confRoot,
		Arch:     *arch,
		Store:    store,
		StubPath: *stubPath,
	})
	if err != nil {
		return fmt.Errorf("build graph: %w", err)
	}

	var blobs []objstore.Hash
	var aliases []objstore.MapAlias

	if *target != "" {
		// Scoped to one node: publish exactly that node's manifest, output
		// blobs, and identity alias. Its inputs were published by the earlier
		// jobs that built the node's dependencies.
		blobs, aliases, err = graphResult.BuiltOutputsFor(store, *target)
		if err != nil {
			return err
		}
	} else {
		// The recorded inputs: source archives and build-tooling .debs, by hash.
		inputs, err := restore.InputHashes(confRoot, *arch)
		if err != nil {
			return fmt.Errorf("enumerate inputs: %w", err)
		}

		// The built outputs: every built node's manifest and output blobs, plus
		// the identity→manifest aliases.
		var outputBlobs []objstore.Hash
		outputBlobs, aliases = graphResult.BuiltOutputs(store)

		// Merge the two enumerations into one deduplicated blob worklist.
		blobs = dedupHashes(append(inputs, outputBlobs...))
	}
	l.Info("publishing to %s: %d blobs, %d aliases (arch %s)", ref, len(blobs), len(aliases), *arch)

	pub, err := objstore.NewPublisher(ref, insecure, store.Blobs)
	if err != nil {
		return fmt.Errorf("open publisher: %w", err)
	}
	progress := func(done, total int, kind, refName, outcome string) {
		l.Info("[%d/%d] %s %s %s", done, total, outcome, kind, refName)
	}
	res, err := pub.Publish(blobs, aliases, progress)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	l.Info("published: %d blobs uploaded, %d already present, %d missing locally; %d aliases set, %d present",
		res.BlobsUploaded, res.BlobsPresent, res.BlobsMissing, res.Aliases, res.AliasesPresent)
	if res.BlobsMissing > 0 {
		l.Warn("%d enumerated blobs were absent from the local store and skipped", res.BlobsMissing)
	}
	return nil
}

// dedupHashes returns hashes with duplicates removed, preserving first-seen
// order.
func dedupHashes(in []objstore.Hash) []objstore.Hash {
	seen := make(map[string]struct{}, len(in))
	out := make([]objstore.Hash, 0, len(in))
	for _, h := range in {
		if _, ok := seen[h.String()]; ok {
			continue
		}
		seen[h.String()] = struct{}{}
		out = append(out, h)
	}
	return out
}
