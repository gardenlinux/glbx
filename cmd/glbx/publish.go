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
	// manifests are the output-manifest hashes of the selected built artifacts —
	// the roots whose SBOM closures get a sbom-<M> tag. SBOM blobs, SBOM map
	// aliases and closures are derived from these and uploaded unconditionally
	// (never withheld by --aliases-to), so --aliases-from carries none.
	manifests []objstore.Hash
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
	jobs := fs.Int("jobs", 0, "max concurrent registry round-trips (0 = default)")
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
		return publishPlan{blobs: blobs, manifests: []objstore.Hash{hash}}, err
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
			blobs, aliases, manifest, err := graph.BuiltOutputsFor(store, target)
			if err != nil {
				return publishPlan{}, err
			}
			return publishPlan{blobs: blobs, aliases: aliases, manifests: []objstore.Hash{manifest}}, nil
		}

		inputs, err := restore.InputHashes(confRoot, arch)
		if err != nil {
			return publishPlan{}, fmt.Errorf("enumerate inputs: %w", err)
		}
		outputs, aliases, manifests := graph.BuiltOutputs(store)
		return publishPlan{
			blobs:     dedupHashes(append(inputs, outputs...)),
			aliases:   aliases,
			manifests: manifests,
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

	// Resolve the SBOM closures of the selected manifests. SBOM document blobs and
	// their map aliases are uploaded unconditionally (never withheld by
	// --aliases-to), and each selected manifest gets a sbom-<M> closure tag.
	// --aliases-from carries no manifests, so this is empty on the alias-only run —
	// the SBOMs were already uploaded by the earlier --aliases-to run.
	sbom := collectSBOMs(store, sbomNamespace(), plan.manifests, l)
	plan.blobs = append(plan.blobs, sbom.blobs...)

	// Withhold the reproducibility-gated graph aliases until a later --aliases-from
	// run, but always publish the SBOM map aliases now.
	publishAliases := append(append([]objstore.MapAlias{}, plan.aliases...), sbom.aliases...)
	if *aliasesTo != "" {
		if err := writeAliasFile(*aliasesTo, plan.aliases); err != nil {
			return err
		}
		l.Info("wrote %d aliases to %s; publishing blobs and %d sbom aliases", len(plan.aliases), *aliasesTo, len(sbom.aliases))
		publishAliases = sbom.aliases
	}

	l.Info("publishing to %s: %d blobs, %d aliases, %d sbom closures (arch %s)",
		ref, len(plan.blobs), len(publishAliases), len(sbom.closures), *arch)

	pub, err := objstore.NewPublisher(ref, insecure, store.Blobs)
	if err != nil {
		return fmt.Errorf("open publisher: %w", err)
	}
	if *jobs > 0 {
		pub.Concurrency = *jobs
	}
	progress := func(done, total int, kind, refName, outcome string) {
		l.Info("[%d/%d] %s %s %s", done, total, outcome, kind, refName)
	}
	res, err := pub.Publish(plan.blobs, publishAliases, progress)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	// Push the sbom-<M> closure tags after their document blobs are up. Each is
	// never overwritten: a prior publisher of the same manifest wins the race.
	closuresSet := 0
	for _, c := range sbom.closures {
		set, err := pub.EnsureSBOMClosure(c.root, c.layers)
		if err != nil {
			return fmt.Errorf("publish sbom closure %s: %w", c.root, err)
		}
		if set {
			closuresSet++
		}
	}
	if len(sbom.closures) > 0 {
		l.Info("sbom closures: %d set, %d already present", closuresSet, len(sbom.closures)-closuresSet)
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

// sbomNamespace is the SBOM namespace this publish operates in. Published SBOMs
// and their closure tags live under it; it mirrors the generator's namespace.
func sbomNamespace() string { return artifact.SBOMNamespace() }

// sbomClosureSpec is one sbom-<root> closure tag to publish and the ordered SPDX
// document layers it aggregates.
type sbomClosureSpec struct {
	root   objstore.Hash
	layers []objstore.SBOMClosureLayer
}

// sbomPublishSet is the SBOM portion of a publish: the document blobs and their
// map aliases (uploaded unconditionally via the ordinary worklist) and the
// per-root closure tags.
type sbomPublishSet struct {
	blobs    []objstore.Hash
	aliases  []objstore.MapAlias
	closures []sbomClosureSpec
}

// collectSBOMs resolves the SPDX import closure of each selected manifest and
// assembles the SBOM publish set: every closure document's blob and K(A,M)→D map
// alias (deduplicated across all closures), plus one closure tag per selected
// root. A manifest whose SBOM is absent locally is skipped with a warning — a
// cache built before SBOMs or whose generation was skipped still publishes its
// payload. Resolution reads only the local store.
func collectSBOMs(store *objstore.Store, namespace string, manifests []objstore.Hash, l *log.Logger) sbomPublishSet {
	var set sbomPublishSet
	seenBlob := map[string]struct{}{}
	seenAlias := map[string]struct{}{}
	seenRoot := map[string]struct{}{}

	for _, m := range manifests {
		if _, done := seenRoot[m.String()]; done {
			continue
		}
		seenRoot[m.String()] = struct{}{}

		refs, found, err := artifact.SBOMClosure(store, namespace, m)
		if err != nil {
			l.Warn("sbom closure for %s incomplete, skipping its sbom: %v", m.Short(), err)
			continue
		}
		if !found {
			l.Warn("no sbom for %s in namespace %q; skipping its sbom", m.Short(), namespace)
			continue
		}

		layers := make([]objstore.SBOMClosureLayer, 0, len(refs))
		for _, r := range refs {
			if _, ok := seenBlob[r.Doc.String()]; !ok {
				seenBlob[r.Doc.String()] = struct{}{}
				set.blobs = append(set.blobs, r.Doc)
			}
			if _, ok := seenAlias[r.MapKey.String()]; !ok {
				seenAlias[r.MapKey.String()] = struct{}{}
				set.aliases = append(set.aliases, objstore.MapAlias{Key: r.MapKey, Value: r.Doc})
			}
			size, err := blobSize(store, r.Doc)
			if err != nil {
				l.Warn("sbom blob %s for %s missing locally; skipping closure of %s", r.Doc.Short(), r.Manifest.Short(), m.Short())
				layers = nil
				break
			}
			layers = append(layers, objstore.SBOMClosureLayer{Doc: r.Doc, Size: size, Manifest: r.Manifest})
		}
		if len(layers) > 0 {
			set.closures = append(set.closures, sbomClosureSpec{root: m, layers: layers})
		}
	}
	return set
}

// blobSize returns the on-disk byte size of a local blob.
func blobSize(store *objstore.Store, h objstore.Hash) (int64, error) {
	p, err := store.Blobs.Path(h)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
