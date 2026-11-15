package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/build"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// cmdSBOM resolves and prints the SBOM document for one artifact result, or —
// with --print-id — just the object-store map key its SBOM is stored under. The
// result is selected either by --target (a build Key, resolved against the
// configuration directory like `glbx build`) or by --manifest (an output-manifest
// hash given directly). The map key is folded from the SBOM seed, the namespace
// ($GLBX_SBOM_NAMESPACE, default localhost), and the manifest hash. Reads go
// through the same store as every other command, so a configured registry is
// consulted via pull-through transparently.
func cmdSBOM(args []string) error {
	fs := flag.NewFlagSet("sbom", flag.ExitOnError)
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture (with --target)")
	cacheDir := fs.String("cache", "", "cache directory")
	confDir := fs.String("conf-dir", "", "configuration directory (with --target)")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary (with --target)")
	target := fs.String("target", "", "artifact Key whose SBOM to resolve (e.g. rootfs:base:arm64); mutually exclusive with --manifest")
	manifest := fs.String("manifest", "", "output-manifest hash whose SBOM to resolve; mutually exclusive with --target")
	printID := fs.Bool("print-id", false, "print only the SBOM map key (seed+namespace+manifest fold), not the document")
	fs.Parse(args)

	if (*target == "") == (*manifest == "") {
		return fmt.Errorf("exactly one of --target or --manifest is required")
	}

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	// Determine the output-manifest hash the SBOM is keyed on: given directly with
	// --manifest, or resolved from the target's identity → map entry.
	var manifestHash objstore.Hash
	if *manifest != "" {
		manifestHash, err = objstore.NewHash(*manifest)
		if err != nil {
			return fmt.Errorf("parse manifest hash: %w", err)
		}
	} else {
		confRoot := *confDir
		if confRoot == "" {
			confRoot = findConfDir()
		}
		if confRoot == "" {
			return fmt.Errorf("cannot find configuration directory; use --conf-dir")
		}
		graphResult, err := build.BuildGraph(build.GraphConfig{
			ConfDir:  confRoot,
			Arch:     *arch,
			Store:    store,
			StubPath: *stubPath,
		})
		if err != nil {
			return fmt.Errorf("build graph: %w", err)
		}
		a := graphResult.Graph.Find(*target)
		if a == nil {
			return fmt.Errorf("no artifact with Key %q in graph", *target)
		}
		identity, err := a.Identity()
		if err != nil {
			return fmt.Errorf("compute identity for %s: %w", *target, err)
		}
		manifestHash, err = store.Map.Get(identity)
		if err != nil {
			return fmt.Errorf("%s is not built (no manifest): %w", *target, err)
		}
	}

	key := artifact.SBOMMapKey(artifact.SBOMNamespace(), manifestHash)

	if *printID {
		fmt.Println(key)
		return nil
	}

	blobHash, err := store.Map.Get(key)
	if err != nil {
		return fmt.Errorf("no SBOM for manifest %s in namespace %q (key %s): %w",
			manifestHash, artifact.SBOMNamespace(), key, err)
	}
	r, err := store.Blobs.Open(blobHash)
	if err != nil {
		return fmt.Errorf("open SBOM blob %s: %w", blobHash, err)
	}
	defer r.Close()
	if _, err := io.Copy(os.Stdout, r); err != nil {
		return fmt.Errorf("write SBOM: %w", err)
	}
	return nil
}
