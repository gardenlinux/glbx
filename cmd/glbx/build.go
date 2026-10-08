package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"runtime"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/build"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/taskui"
	"golang.org/x/term"
)

func cmdBuild(args []string) error {
	if len(args) >= 2 && args[0] == "--view-logs" {
		return cmdBuildViewLogs(args[1])
	}

	defaultJobs := int(math.Sqrt(float64(runtime.NumCPU())))

	fs := flag.NewFlagSet("build", flag.ExitOnError)
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	jobs := fs.Int("jobs", defaultJobs, "parallel jobs")
	cacheDir := fs.String("cache", "", "cache directory")
	confDir := fs.String("conf-dir", "", "configuration directory")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	invalidate := fs.String("invalidate", "", "delete the map entry for the target with this Key (e.g. rootfs:amd64) and exit")
	target := fs.String("target", "", "build only the node with this Key (e.g. rootfs:amd64); other nodes must resolve from cache")
	noRecurse := fs.Bool("no-recurse", false, "require every non-target node to resolve from cache; a miss is a hard error")
	stream := fs.Bool("stream", false, "forward the target's logs live to the console instead of the task UI (requires --target and --no-recurse)")
	logsOutput := fs.String("logs-output", "", "path to write the build-logs JSON snapshot (default: $TMPDIR/glbx-build-*.json)")
	fs.Parse(args)

	if *jobs <= 0 {
		*jobs = defaultJobs
	}

	storeDir := *cacheDir
	store, err := openStore(storeDir)
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

	ctx, l := rootContext(log.Engine)

	graphResult, err := build.BuildGraph(build.GraphConfig{
		ConfDir:  confRoot,
		Arch:     *arch,
		Store:    store,
		StubPath: *stubPath,
	})
	if err != nil {
		return fmt.Errorf("build graph: %w", err)
	}

	if *invalidate != "" {
		a := graphResult.Graph.Find(*invalidate)
		if a == nil {
			return fmt.Errorf("no artifact with Key %q in graph", *invalidate)
		}
		identity, err := a.Identity()
		if err != nil {
			return fmt.Errorf("compute identity for %s: %w", *invalidate, err)
		}
		if !store.Map.Has(identity) {
			l.Info("no map entry for %s (identity %s) — nothing to invalidate", *invalidate, identity)
			return nil
		}
		if err := store.Map.Delete(identity); err != nil {
			return fmt.Errorf("delete map entry %s: %w", identity, err)
		}
		l.Info("invalidated %s (identity %s)", *invalidate, identity)
		return nil
	}

	l.Info("graph: %d nodes", graphResult.Graph.Len())

	if *target != "" {
		if graphResult.Graph.Find(*target) == nil {
			return fmt.Errorf("no artifact with Key %q in graph", *target)
		}
	} else if *noRecurse {
		return fmt.Errorf("--no-recurse requires --target")
	}

	if *stream && (*target == "" || !*noRecurse) {
		return fmt.Errorf("--stream requires --target and --no-recurse")
	}

	engine := artifact.NewEngine(graphResult.Graph, store, *jobs)
	if *target != "" {
		engine.SetScope(*target, *noRecurse)
	}
	engine.SetStream(*stream)
	results, err := engine.RunWithUI(ctx, *logsOutput)
	if err != nil {
		return fmt.Errorf("run engine: %w", err)
	}

	// With a single target, only the target's own result decides success: the
	// engine still walks the whole graph, so unrelated nodes that cannot resolve
	// from cache under --no-recurse report errors that are not this job's
	// concern. A real missing dependency still fails the target, because the
	// engine skips a node whose dependency was absent.
	if *target != "" {
		var targetResult *artifact.BuildResult
		for i := range results {
			if results[i].Artifact.Key() == *target {
				targetResult = &results[i]
				break
			}
		}
		if targetResult == nil {
			return fmt.Errorf("target %s produced no result", *target)
		}
		if targetResult.Err != nil {
			return fmt.Errorf("%s: %w", *target, targetResult.Err)
		}
		return nil
	}

	var failed int
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}

	rootfsID, err := graphResult.Rootfs.Identity()
	if err == nil {
		l.Info("rootfs identity: %s", rootfsID)
	}

	if failed > 0 {
		return fmt.Errorf("%d artifact(s) failed", failed)
	}
	return nil
}

func cmdBuildViewLogs(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	tracker, err := taskui.Deserialize(f)
	if err != nil {
		return fmt.Errorf("deserialize %s: %w", path, err)
	}

	if !term.IsTerminal(int(os.Stderr.Fd())) {
		tracker.PrintPlain()
		return nil
	}

	overview := taskui.NewTaskOverview(tracker)
	overview.OnEnter = func(task *taskui.Task, stop <-chan struct{}) {
		fmt.Fprintf(os.Stderr, "--- logs: %s (press q to return) ---\n", task.Name)
		printer := log.NewLogPrinter(task.Log)
		printer.Run()

		done := make(chan struct{})
		go func() {
			buf := make([]byte, 1)
			for {
				n, err := os.Stdin.Read(buf)
				if err != nil {
					break
				}
				if n == 1 && buf[0] == 'q' {
					break
				}
			}
			close(done)
		}()

		select {
		case <-done:
		case <-stop:
		}
		printer.Stop()
		fmt.Fprintf(os.Stderr, "--- end logs ---\n")
	}
	overview.Show()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig

	overview.Hide()
	return nil
}
