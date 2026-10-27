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
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/taskui"
	"golang.org/x/term"
)

func cmdBuild(args []string) error {
	if len(args) >= 1 && args[0] == "--view-logs" {
		return cmdBuildViewLogs(args[1:])
	}

	defaultJobs := int(math.Sqrt(float64(runtime.NumCPU())))

	fs := flag.NewFlagSet("build", flag.ExitOnError)
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	jobs := fs.Int("jobs", defaultJobs, "parallel jobs")
	cacheDir := fs.String("cache", "", "cache directory")
	confDir := fs.String("conf-dir", "", "configuration directory")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	invalidate := fs.String("invalidate", "", "delete the map entry for the target with this Key (e.g. rootfs:amd64) and exit")
	listOutputs := fs.String("list-outputs", "", "print the output names of the already-built target with this Key (e.g. rootfs:amd64) and exit")
	outputPath := fs.String("output-path", "", "print the cache path of one named output of the already-built target with this Key; the output name is a positional argument")
	target := fs.String("target", "", "build only the node with this Key (e.g. rootfs:amd64); other nodes must resolve from cache")
	noRecurse := fs.Bool("no-recurse", false, "require every non-target node to resolve from cache; a miss is a hard error")
	stream := fs.Bool("stream", false, "forward the target's logs live to the console instead of the task UI (requires --target and --no-recurse)")
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

	if *listOutputs != "" {
		a := graphResult.Graph.Find(*listOutputs)
		if a == nil {
			return fmt.Errorf("no artifact with Key %q in graph", *listOutputs)
		}
		outputs, err := artifact.ResolveOutputs(a, store)
		if err != nil {
			return err
		}
		for _, o := range outputs {
			fmt.Println(o.Name)
		}
		return nil
	}

	if *outputPath != "" {
		if len(fs.Args()) != 1 {
			return fmt.Errorf("--output-path requires exactly one output name as a positional argument")
		}
		name := fs.Args()[0]
		a := graphResult.Graph.Find(*outputPath)
		if a == nil {
			return fmt.Errorf("no artifact with Key %q in graph", *outputPath)
		}
		outputs, err := artifact.ResolveOutputs(a, store)
		if err != nil {
			return err
		}
		for _, o := range outputs {
			if o.Name == name {
				path, err := store.Blobs.Path(o.Hash)
				if err != nil {
					return fmt.Errorf("resolve path for output %q of %s: %w", name, *outputPath, err)
				}
				fmt.Println(path)
				return nil
			}
		}
		return fmt.Errorf("%s has no output named %q", *outputPath, name)
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
	results, err := engine.RunWithUI(ctx)
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

// cmdBuildViewLogs renders stored build logs. Without --run it works from the
// current configuration tree: each node's state comes from whether its output
// manifest exists (built → green, log-but-no-manifest → failed, neither →
// pending) and its log is loaded from the cache by identity. With --run <id> the
// node list, states, and logs all come from a recorded run instead. A positional
// <target> prints that one node's log and exits; --engine-logs (only with --run)
// prints the run's engine narrative.
func cmdBuildViewLogs(args []string) error {
	fs := flag.NewFlagSet("build --view-logs", flag.ExitOnError)
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	cacheDir := fs.String("cache", "", "cache directory")
	confDir := fs.String("conf-dir", "", "configuration directory")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	runID := fs.String("run", "", "view logs recorded under this build run id")
	engineLogs := fs.Bool("engine-logs", false, "print the run's engine scheduling log (requires --run)")
	fs.Parse(args)

	var target string
	if rest := fs.Args(); len(rest) == 1 {
		target = rest[0]
	} else if len(rest) > 1 {
		return fmt.Errorf("--view-logs takes at most one target name")
	}

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	if *engineLogs && *runID == "" {
		return fmt.Errorf("--engine-logs requires --run")
	}

	if *runID != "" {
		return viewRunLogs(store, *runID, target, *engineLogs)
	}
	return viewTreeLogs(store, *confDir, *arch, *stubPath, target)
}

// viewTreeLogs renders logs for the current tree. With a target it prints that
// node's log; otherwise it shows the interactive overview over every node.
func viewTreeLogs(store *objstore.Store, confDir, arch, stubPath, target string) error {
	confRoot := confDir
	if confRoot == "" {
		confRoot = findConfDir()
	}
	if confRoot == "" {
		return fmt.Errorf("cannot find configuration directory; use --conf-dir")
	}

	graphResult, err := build.BuildGraph(build.GraphConfig{
		ConfDir:  confRoot,
		Arch:     arch,
		Store:    store,
		StubPath: stubPath,
	})
	if err != nil {
		return fmt.Errorf("build graph: %w", err)
	}
	graph := graphResult.Graph

	if target != "" {
		a := graph.Find(target)
		if a == nil {
			return fmt.Errorf("no artifact with Key %q in graph", target)
		}
		buf := log.NewBufferTarget()
		if err := artifact.LoadNodeLog(store, a, buf); err != nil {
			return fmt.Errorf("no log for %s: %w", target, err)
		}
		log.PrintBuffer(buf)
		return nil
	}

	tracker := taskui.NewTaskTracker()
	for _, key := range graph.Keys() {
		a := graph.Find(key)
		t := tracker.Add(key)
		// A present output manifest means the node is built (success). Otherwise
		// a stored log means a failed attempt, and no log means the node was
		// never built on this tree. In every case load whatever log exists so a
		// selected node shows its output; a success node whose log was never
		// recorded stays empty.
		built := false
		if _, err := artifact.ResolveOutputs(a, store); err == nil {
			built = true
		}
		hasLog := artifact.LoadNodeLog(store, a, t.Log) == nil
		switch {
		case built:
			t.SetState(taskui.Success)
		case hasLog:
			t.SetState(taskui.Failed)
		}
	}
	return showLogOverview(tracker)
}

// viewRunLogs renders logs from a recorded run. With a target it prints that
// node's log; with engineLogs it prints the run's engine narrative; otherwise it
// shows the interactive overview over the run's nodes.
func viewRunLogs(store *objstore.Store, runID, target string, engineLogs bool) error {
	rec, err := artifact.LoadRunRecord(store, runID)
	if err != nil {
		return err
	}

	if engineLogs {
		buf := log.NewBufferTarget()
		if rec.EngineLog.IsZero() {
			return fmt.Errorf("run %s recorded no engine log", runID)
		}
		if err := artifact.LoadLogBlob(store, rec.EngineLog, buf); err != nil {
			return fmt.Errorf("load engine log for run %s: %w", runID, err)
		}
		log.PrintBuffer(buf)
		return nil
	}

	if target != "" {
		for _, n := range rec.Nodes {
			if n.Key != target {
				continue
			}
			if n.Log.IsZero() {
				return fmt.Errorf("run %s recorded no log for %s", runID, target)
			}
			buf := log.NewBufferTarget()
			if err := artifact.LoadLogBlob(store, n.Log, buf); err != nil {
				return fmt.Errorf("load log for %s: %w", target, err)
			}
			log.PrintBuffer(buf)
			return nil
		}
		return fmt.Errorf("run %s has no node %q", runID, target)
	}

	tracker := artifact.TrackerFromRun(store, rec)
	return showLogOverview(tracker)
}

// showLogOverview runs the interactive overview over a populated tracker: a list
// of nodes whose per-node log prints on selection. Non-interactive callers get
// the plain one-shot list.
func showLogOverview(tracker *taskui.TaskTracker) error {
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
