package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/build"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/restore"
)

type publishPlan struct {
	blobs   []objstore.Hash
	aliases []objstore.MapAlias
}

type aliasJSON struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

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

func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	confDir := fs.String("conf-dir", "", "configuration directory")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	registry := fs.String("registry", "", "target registry reference host[:port]/repo (default $GLBX_REGISTRY)")
	target := fs.String("target", "", "publish only the built node with this Key (e.g. rootfs:base:amd64)")
	manifestHex := fs.String("manifest", "", "publish the manifest blob with this hash (64-char hex) and every blob it references; no alias")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	aliasesTo := fs.String("aliases-to", "", "publish blobs only and write the map aliases as a JSON array to this file instead of setting them on the registry")
	aliasesFrom := fs.String("aliases-from", "", "publish only the map aliases read from this JSON file (set by an earlier --aliases-to run); no blobs")
	fs.Parse(args)

	if *aliasesTo != "" && *aliasesFrom != "" {
		return fmt.Errorf("--aliases-to and --aliases-from are mutually exclusive")
	}

	ref, insecure := registryFromEnv()
	if *registry != "" {
		ref = *registry
	}
	if ref == "" {
		return fmt.Errorf("no registry given; pass --registry or set GLBX_REGISTRY")
	}

	_, l := rootContext(log.Engine)

	storeDir := *cacheDir
	if storeDir == "" {
		storeDir = objstore.DefaultRoot()
	}
	store, err := objstore.NewLocal(storeDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	fromAliases := func(path string) (publishPlan, error) {
		aliases, err := readAliasFile(path)
		return publishPlan{aliases: aliases}, err
	}

	fromManifest := func(hex string) (publishPlan, error) {
		hash, err := objstore.NewHash(hex)
		if err != nil {
			return publishPlan{}, fmt.Errorf("--manifest: %w", err)
		}
		blobs, err := artifact.ManifestBlobs(store, hash)
		return publishPlan{blobs: blobs}, err
	}

	fromGraph := func(confDir, arch, stubPath, target string) (publishPlan, error) {
		confRoot := confDir
		if confRoot == "" {
			confRoot = findConfDir()
		}
		if confRoot == "" {
			return publishPlan{}, fmt.Errorf("cannot find configuration directory; use --conf-dir")
		}

		graph, err := build.BuildGraph(build.GraphConfig{
			ConfDir:    confRoot,
			Arch:       arch,
			Store:      store,
			StubPath:   stubPath,
			ScratchDir: scratchDir(storeDir),
		})
		if err != nil {
			return publishPlan{}, fmt.Errorf("build graph: %w", err)
		}

		if target != "" {
			blobs, aliases, err := graph.BuiltOutputsFor(store, target)
			return publishPlan{blobs: blobs, aliases: aliases}, err
		}

		inputs, err := restore.InputHashes(confRoot, arch)
		if err != nil {
			return publishPlan{}, fmt.Errorf("enumerate inputs: %w", err)
		}
		outputs, aliases := graph.BuiltOutputs(store)
		return publishPlan{
			blobs:   dedupHashes(append(inputs, outputs...)),
			aliases: aliases,
		}, nil
	}

	var plan publishPlan
	switch {
	case *aliasesFrom != "":
		plan, err = fromAliases(*aliasesFrom)
	case *manifestHex != "":
		plan, err = fromManifest(*manifestHex)
	default:
		plan, err = fromGraph(*confDir, *arch, *stubPath, *target)
	}
	if err != nil {
		return err
	}

	// Withhold aliases until the reproducibility check passes.
	if *aliasesTo != "" {
		if err := writeAliasFile(*aliasesTo, plan.aliases); err != nil {
			return err
		}
		l.Info("wrote %d aliases to %s; publishing blobs only", len(plan.aliases), *aliasesTo)
		plan.aliases = nil
	}

	l.Info("publishing to %s: %d blobs, %d aliases (arch %s)", ref, len(plan.blobs), len(plan.aliases), *arch)

	pub, err := objstore.NewPublisher(ref, insecure, store.Blobs)
	if err != nil {
		return fmt.Errorf("open publisher: %w", err)
	}
	progress := func(done, total int, kind, refName, outcome string) {
		l.Info("[%d/%d] %s %s %s", done, total, outcome, kind, refName)
	}
	res, err := pub.Publish(plan.blobs, plan.aliases, progress)
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

func dedupHashes(in []objstore.Hash) []objstore.Hash {
	seen := make(map[string]struct{}, len(in))
	out := make([]objstore.Hash, 0, len(in))
	for _, h := range in {
		s := h.String()
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, h)
	}
	return out
}
