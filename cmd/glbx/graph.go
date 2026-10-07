package main

import (
	"flag"
	"fmt"
	"os"

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
	confDir := fs.String("conf-dir", "", "configuration directory (contains pkgs/, rootfs.yml)")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	outputFile := fs.String("output", "", "output file (default: stdout)")
	gantt := fs.String("gantt", "", "render mermaid gantt chart from a build logs file (skips graph build)")
	fs.Parse(args)

	if *gantt != "" {
		return cmdGraphGantt(*gantt, *outputFile)
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

	mermaid := graphResult.Graph.Mermaid()

	var content string
	content = "# Build Dependency Graph\n\n"
	content += fmt.Sprintf("Nodes: %d\n\n", graphResult.Graph.Len())
	content += "```mermaid\n"
	content += mermaid
	content += "```\n"

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
