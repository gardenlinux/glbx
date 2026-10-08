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
	"github.com/gardenlinux/glbx/internal/taskui"
)

func cmdGraph(args []string) error {
	fs := flag.NewFlagSet("graph", flag.ExitOnError)
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	cacheDir := fs.String("cache", "", "cache directory")
	confDir := fs.String("conf-dir", "", "configuration directory")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	outputFile := fs.String("output", "", "output file (default: stdout)")
	format := fs.String("format", "mermaid", "output format: mermaid or json")
	checkBuilt := fs.Bool("check-built", false, "with --format=json, add a per-node built map (true = the node's identity is present in $GLBX_REGISTRY)")
	gantt := fs.String("gantt", "", "render mermaid gantt chart from a build logs file (skips graph build)")
	fs.Parse(args)

	if *gantt != "" {
		return cmdGraphGantt(*gantt, *outputFile)
	}

	if *format != "mermaid" && *format != "json" {
		return fmt.Errorf("unknown format %q (want mermaid or json)", *format)
	}
	if *checkBuilt && *format != "json" {
		return fmt.Errorf("--check-built requires --format=json")
	}

	storeDir := *cacheDir
	if storeDir == "" {
		storeDir = objstore.DefaultRoot()
	}
	store, err := objstore.NewLocal(storeDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

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

	var content string
	if *format == "json" {
		var builtStatus map[string]bool
		if *checkBuilt {
			builtStatus, err = checkBuiltStatus(graphResult.Graph)
			if err != nil {
				return err
			}
		}
		content, err = graphJSON(graphResult.Graph, builtStatus)
		if err != nil {
			return err
		}
	} else {
		content = "# Build Dependency Graph\n\n"
		content += fmt.Sprintf("Nodes: %d\n\n", graphResult.Graph.Len())
		content += "```mermaid\n"
		content += graphResult.Graph.Mermaid()
		content += "```\n"
	}

	if *outputFile != "" {
		if err := os.WriteFile(*outputFile, []byte(content), 0644); err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		_, l := rootContext(log.Engine)
		l.Info("graph written to %s", *outputFile)
	} else {
		fmt.Print(content)
	}

	return nil
}

// graphExport is the canonical serialized form of the dependency graph: node
// Keys in stable topological order and built-from edges sorted deterministically.
// It records structure only (no identities), so it changes when the graph's
// shape changes, not on every source edit. BuiltStatus is populated only by
// --check-built and is omitted otherwise, keeping the plain export stable.
type graphExport struct {
	Nodes       []string          `json:"nodes"`
	Edges       []graphExportEdge `json:"edges"`
	BuiltStatus map[string]bool   `json:"builtStatus,omitempty"`
}

type graphExportEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// graphJSON renders the graph as deterministic, machine-readable JSON with a
// trailing newline. builtStatus, when non-nil, is attached as the per-node
// built map.
func graphJSON(g *artifact.Graph, builtStatus map[string]bool) (string, error) {
	order := g.StableTopologicalOrder()
	nodes := make([]string, len(order))
	for i, a := range order {
		nodes[i] = a.Key()
	}
	edges := g.Edges()
	exp := graphExport{Nodes: nodes, Edges: make([]graphExportEdge, len(edges)), BuiltStatus: builtStatus}
	for i, e := range edges {
		exp.Edges[i] = graphExportEdge{From: e.From, To: e.To}
	}
	b, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal graph: %w", err)
	}
	return string(b) + "\n", nil
}

// checkBuiltStatus reports, per node Key, whether that node's identity is
// already present in the configured registry (a map/<identity> tag exists). It
// probes the registry directly — not a pull-through — so it is a pure set of
// existence checks that fetches nothing. With no registry configured every node
// is reported not-built.
func checkBuiltStatus(g *artifact.Graph) (map[string]bool, error) {
	status := make(map[string]bool, g.Len())

	ref, insecure := registryFromEnv()
	if ref == "" {
		for _, key := range g.Keys() {
			status[key] = false
		}
		return status, nil
	}

	registry, err := objstore.NewRegistry(ref, insecure)
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}

	for _, key := range g.Keys() {
		a := g.Find(key)
		identity, err := a.Identity()
		if err != nil {
			return nil, fmt.Errorf("identity for %s: %w", key, err)
		}
		status[key] = registry.Map.Has(identity)
	}
	return status, nil
}

func cmdGraphGantt(logsPath, outputFile string) error {
	f, err := os.Open(logsPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", logsPath, err)
	}
	defer f.Close()

	tracker, err := taskui.Deserialize(f)
	if err != nil {
		return fmt.Errorf("deserialize %s: %w", logsPath, err)
	}

	mermaid := tracker.Gantt()

	content := "# Build Timeline\n\n"
	content += "```mermaid\n"
	content += mermaid
	content += "```\n"

	if outputFile != "" {
		if err := os.WriteFile(outputFile, []byte(content), 0644); err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		_, l := rootContext(log.Engine)
		l.Info("gantt written to %s", outputFile)
	} else {
		fmt.Print(content)
	}

	return nil
}
