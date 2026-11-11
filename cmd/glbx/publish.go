package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/gardenlinux/glbx/internal/build"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/restore"
)

// aliasJSON is the on-disk form of one map alias: the map key (an artifact
// identity) and the value hash it points at, both as 64-char hex strings. A
// publish --aliases-to run writes a JSON array of these instead of setting the
// map tags on the registry; a later publish --aliases-from run reads it back and
// sets exactly those tags, and cache map set --from injects them into the local
// map. The array is normally the one identity→manifest alias of a single built
// node (plus its log alias when present), so it is small enough to pass through a
// GitHub Actions job output.
type aliasJSON struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// writeAliasFile serializes aliases as a JSON array to path.
func writeAliasFile(path string, aliases []objstore.MapAlias) error {
	out := make([]aliasJSON, len(aliases))
	for i, a := range aliases {
		out[i] = aliasJSON{Key: a.Key.String(), Value: a.Value.String()}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal aliases: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write aliases to %s: %w", path, err)
	}
	return nil
}

// readAliasFile parses the JSON array written by writeAliasFile back into
// MapAlias values, validating each hash.
func readAliasFile(path string) ([]objstore.MapAlias, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read aliases from %s: %w", path, err)
	}
	var in []aliasJSON
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, fmt.Errorf("parse aliases from %s: %w", path, err)
	}
	aliases := make([]objstore.MapAlias, len(in))
	for i, a := range in {
		key, err := objstore.NewHash(a.Key)
		if err != nil {
			return nil, fmt.Errorf("alias %d key: %w", i, err)
		}
		value, err := objstore.NewHash(a.Value)
		if err != nil {
			return nil, fmt.Errorf("alias %d value: %w", i, err)
		}
		aliases[i] = objstore.MapAlias{Key: key, Value: value}
	}
	return aliases, nil
}

// cmdPublish mirrors a checkout's locally-held content to an OCI registry. It
// reads from the local store and writes to the registry, uploading only the
// blobs and aliases the registry lacks. The worklist is the union of the two
// enumerations the system already defines: the recorded inputs restore collects
// and the built outputs garbage collection walks. With --target it is scoped to
// a single built node's manifest, output blobs, and identity alias.
func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	confDir := fs.String("conf-dir", "", "configuration directory")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	registry := fs.String("registry", "", "target registry reference host[:port]/repo (default $GLBX_REGISTRY)")
	target := fs.String("target", "", "publish only the built node with this Key (e.g. rootfs:base:amd64)")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	aliasesTo := fs.String("aliases-to", "", "publish blobs only and write the map aliases as a JSON array to this file instead of setting them on the registry")
	aliasesFrom := fs.String("aliases-from", "", "publish only the map aliases read from this JSON file (set by an earlier --aliases-to run); no blobs, no graph walk")
	fs.Parse(args)

	if *aliasesTo != "" && *aliasesFrom != "" {
		return fmt.Errorf("--aliases-to and --aliases-from are mutually exclusive")
	}

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

	_, l := rootContext(log.Engine)

	// --aliases-from: publish only the map aliases from the file, no blobs and no
	// graph walk. The aliases' value blobs were already uploaded by the earlier
	// --aliases-to run, so the publisher's blob source is a pull-through store:
	// EnsureAlias reads each value blob to size its manifest descriptor, and the
	// pull-through resolves it from the registry on this cold runner.
	if *aliasesFrom != "" {
		aliases, err := readAliasFile(*aliasesFrom)
		if err != nil {
			return err
		}
		store, err := openStore(*cacheDir)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		pub, err := objstore.NewPublisher(ref, insecure, store.Blobs)
		if err != nil {
			return fmt.Errorf("open publisher: %w", err)
		}
		progress := func(done, total int, kind, refName, outcome string) {
			l.Info("[%d/%d] %s %s %s", done, total, outcome, kind, refName)
		}
		res, err := pub.Publish(nil, aliases, progress)
		if err != nil {
			return fmt.Errorf("publish: %w", err)
		}
		l.Info("published %d aliases set, %d already present", res.Aliases, res.AliasesPresent)
		return nil
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

	graphResult, err := build.BuildGraph(build.GraphConfig{
		ConfDir:    confRoot,
		Arch:       *arch,
		Store:      store,
		StubPath:   *stubPath,
		ScratchDir: scratchDir(storeDir),
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
	// --aliases-to: withhold the map aliases from the registry. Publish the
	// blobs now, but write the aliases to the file for a later --aliases-from run
	// to set once the node's reproducibility check has passed. Leaving the map
	// tags unset keeps graph --check-built reporting the node not-built, so a node
	// that fails its reproducibility check is rebuilt and rechecked next run
	// rather than latched as built.
	if *aliasesTo != "" {
		if err := writeAliasFile(*aliasesTo, aliases); err != nil {
			return err
		}
		l.Info("wrote %d aliases to %s; publishing blobs only", len(aliases), *aliasesTo)
		aliases = nil
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
