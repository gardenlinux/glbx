package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/build"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/taskui"
	"golang.org/x/term"
)

// cmdBuildCheckReproducible rebuilds already-built targets and checks that each
// reproduces its cached output. With --target it checks one node; otherwise it
// checks every node a whole-tree build would build. The run is flat: every node
// rebuilds from its dependencies' original cached outputs, in parallel bounded by
// --jobs, so a non-reproducible dependency never cascades into its dependents. A
// JSON report pairing each artifact's original and reproduced manifest hash is
// written to --report; the live task UI shows per-node green (reproduced) or red
// (mismatch or rebuild failure).
func cmdBuildCheckReproducible(args []string) error {
	defaultJobs := int(math.Sqrt(float64(runtime.NumCPU())))

	fs := flag.NewFlagSet("build --check-reproducible", flag.ExitOnError)
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	jobs := fs.Int("jobs", defaultJobs, "parallel jobs")
	cacheDir := fs.String("cache", "", "cache directory")
	confDir := fs.String("conf-dir", "", "configuration directory")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	target := fs.String("target", "", "check only the node with this Key (e.g. rootfs:amd64); otherwise check every node")
	report := fs.String("report", "", "write the JSON reproducibility report to this file (required)")
	fs.Parse(args)

	if *report == "" {
		return fmt.Errorf("--check-reproducible requires --report <file>")
	}
	if *jobs <= 0 {
		*jobs = defaultJobs
	}

	store, err := openStore(*cacheDir)
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

	ctx, l := rootContext(log.Reprod)

	graphResult, err := build.BuildGraph(build.GraphConfig{
		ConfDir:  confRoot,
		Arch:     *arch,
		Store:    store,
		StubPath: *stubPath,
	})
	if err != nil {
		return fmt.Errorf("build graph: %w", err)
	}
	graph := graphResult.Graph
	if err := graph.Build(); err != nil {
		return fmt.Errorf("build graph: %w", err)
	}

	var targets []string
	if *target != "" {
		if graph.Find(*target) == nil {
			return fmt.Errorf("no artifact with Key %q in graph", *target)
		}
		targets = []string{*target}
	} else {
		targets = graph.Keys()
	}
	l.Info("checking reproducibility of %d target(s)", len(targets))

	// Drive the same live UI the build engine uses: one task per target, green on
	// a reproduced node, red on a mismatch or a rebuild failure. The runner's
	// hooks route each node's log into its task buffer and report its outcome.
	tracker := taskui.NewTaskTracker()
	taskMap := make(map[string]*taskui.Task, len(targets))
	for _, key := range targets {
		taskMap[key] = tracker.Add(key)
	}

	interactive := term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
	var overview *taskui.TaskOverview
	var viewer *taskui.NonInteractiveViewer
	if interactive {
		overview = newLogOverview(tracker)
		overview.Show()
	} else {
		viewer = taskui.NewNonInteractiveViewer(tracker)
		viewer.Start()
	}

	hooks := artifact.ReproHooks{
		NodeStarted: func(key string) context.Context {
			t := taskMap[key]
			t.SetState(taskui.InProgress)
			return log.WithTarget(ctx, t.Log)
		},
		NodeFinished: func(r artifact.ReproResult) {
			t := taskMap[r.Key]
			if r.Err == nil && r.Match {
				t.SetState(taskui.Success)
			} else {
				t.SetState(taskui.Failed)
			}
		},
	}

	results, runErr := artifact.CheckReproducible(ctx, graph, store, targets, *jobs, hooks)

	if interactive {
		overview.Hide()
	} else {
		viewer.Stop()
	}

	if runErr != nil {
		return runErr
	}

	if err := writeReproReport(*report, *arch, results); err != nil {
		return fmt.Errorf("write report: %w", err)
	}

	var mismatched, failed int
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed++
		case !r.Match:
			mismatched++
		}
	}

	fmt.Fprintf(os.Stderr, "\n")
	tracker.PrintPlain()
	fmt.Fprintf(os.Stderr, "\nChecked %d target(s): %d reproduced, %d non-reproducible, %d failed to rebuild.\nReport written to %s\n",
		len(results), len(results)-mismatched-failed, mismatched, failed, *report)

	if mismatched > 0 || failed > 0 {
		return fmt.Errorf("%d non-reproducible, %d failed to rebuild", mismatched, failed)
	}
	return nil
}

// reproReport is the on-disk JSON form of a reproducibility run: run metadata and
// one entry per checked artifact.
type reproReport struct {
	Arch    string            `json:"arch"`
	Targets int               `json:"targets"`
	Nodes   []reproReportNode `json:"nodes"`
}

// reproReportNode is one artifact's entry. State is "match", "mismatch", or
// "error". ReproducedManifest is omitted on a rebuild error. Log is the hash of
// the rebuild's log blob — the only reference to it, as no map entry points at a
// reproducibility log.
type reproReportNode struct {
	Key                string `json:"key"`
	ShortName          string `json:"short_name"`
	State              string `json:"state"`
	OriginalManifest   string `json:"original_manifest"`
	ReproducedManifest string `json:"reproduced_manifest,omitempty"`
	Log                string `json:"log,omitempty"`
	Error              string `json:"error,omitempty"`
}

// writeReproReport marshals the results to JSON and writes them atomically to
// path (temp file in the same directory, then rename).
func writeReproReport(path, arch string, results []artifact.ReproResult) error {
	rep := reproReport{Arch: arch, Targets: len(results)}
	for _, r := range results {
		n := reproReportNode{
			Key:              r.Key,
			ShortName:        r.ShortName,
			OriginalManifest: r.OriginalManifest.String(),
		}
		switch {
		case r.Err != nil:
			n.State = "error"
			n.Error = r.Err.Error()
		case r.Match:
			n.State = "match"
			n.ReproducedManifest = r.ReproducedManifest.String()
		default:
			n.State = "mismatch"
			n.ReproducedManifest = r.ReproducedManifest.String()
		}
		if !r.Log.IsZero() {
			n.Log = r.Log.String()
		}
		rep.Nodes = append(rep.Nodes, n)
	}

	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize report: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".glbx-repro-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// loadReproReport reads and decodes a reproducibility report from path.
func loadReproReport(path string) (*reproReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rep reproReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("decode report %s: %w", path, err)
	}
	return &rep, nil
}

// trackerFromReport reconstructs a task tracker from a reproducibility report:
// one task per node in the state the check left it in (match → success, mismatch
// or error → failed), with its log loaded from the store by the blob hash the
// report records. It is the report-sourced analog of artifact.TrackerFromRun and
// backs the report-scoped log overview.
func trackerFromReport(store *objstore.Store, rep *reproReport) (*taskui.TaskTracker, error) {
	tracker := taskui.NewTaskTracker()
	for _, n := range rep.Nodes {
		t := tracker.Add(n.Key)
		if n.State == "match" {
			t.SetState(taskui.Success)
		} else {
			t.SetState(taskui.Failed)
		}
		if n.Log == "" {
			continue
		}
		h, err := objstore.NewHash(n.Log)
		if err != nil {
			return nil, fmt.Errorf("node %s: invalid log hash %q: %w", n.Key, n.Log, err)
		}
		_ = artifact.LoadLogBlob(store, h, t.Log)
	}
	return tracker, nil
}

// newLogOverview builds the interactive overview used by the reproducibility run:
// a list of nodes whose per-node log prints on selection, exactly as the build
// engine's overview does.
func newLogOverview(tracker *taskui.TaskTracker) *taskui.TaskOverview {
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
	return overview
}
