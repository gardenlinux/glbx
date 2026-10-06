package artifact

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/taskui"
	"golang.org/x/term"
)

type BuildResult struct {
	Artifact Artifact
	Err      error
	Cached   bool
}

type Engine struct {
	graph   *Graph
	store   *objstore.Store
	workers int
	results []BuildResult
}

func NewEngine(g *Graph, store *objstore.Store, workers int) *Engine {
	if workers <= 0 {
		workers = 1
	}
	return &Engine{
		graph:   g,
		store:   store,
		workers: workers,
	}
}

// runHooks observes engine state transitions during run().
//
// nodeStarted is invoked BEFORE the node's Build() runs, without the engine
// mutex held. It may return a context wrapping the parent ctx (for example,
// log targeting). When nil, the parent ctx is passed through unchanged.
//
// nodeFinished and nodeSkipped run WITH the engine mutex held. Implementations
// must not block or call back into the engine. They observe state; they do
// not record results — the engine appends to e.results itself.
type runHooks struct {
	nodeStarted  func(n *node) context.Context
	nodeFinished func(n *node, result BuildResult)
	nodeSkipped  func(n *node, reason string)
}

func (e *Engine) Run(ctx context.Context) ([]BuildResult, error) {
	return e.run(ctx, runHooks{})
}

// RunWithUI executes the build graph with an interactive task TUI. Each artifact
// becomes a task shown in dependency order. Builds run with the configured worker
// parallelism, and all logs are captured per-task for interactive inspection.
//
// logsOutput, if non-empty, names the path the JSON task-tracker snapshot will
// be written to. Empty falls back to an os.CreateTemp file under the system
// tempdir. The caller controls the path so it can persist alongside the build
// (e.g. into a results-tagged commit).
func (e *Engine) RunWithUI(ctx context.Context, logsOutput string) ([]BuildResult, error) {
	if err := e.graph.Build(); err != nil {
		return nil, err
	}
	if len(e.graph.nodes) == 0 {
		return nil, nil
	}

	order := e.graph.StableTopologicalOrder()

	tracker := taskui.NewTaskTracker()
	taskMap := make(map[string]*taskui.Task, len(order))
	for _, a := range order {
		t := tracker.Add(a.Key())
		taskMap[a.Key()] = t
	}

	interactive := term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
	var overview *taskui.TaskOverview
	var viewer *taskui.NonInteractiveViewer

	if interactive {
		overview = taskui.NewTaskOverview(tracker)
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
	} else {
		viewer = taskui.NewNonInteractiveViewer(tracker)
		viewer.Start()
	}

	hooks := runHooks{
		nodeStarted: func(n *node) context.Context {
			t := taskMap[n.artifact.Key()]
			t.SetState(taskui.InProgress)
			return log.WithTarget(ctx, t.Log)
		},
		nodeFinished: func(n *node, result BuildResult) {
			t := taskMap[n.artifact.Key()]
			if result.Err != nil {
				t.SetState(taskui.Failed)
			} else {
				t.SetState(taskui.Success)
			}
		},
		nodeSkipped: func(n *node, reason string) {
			t, ok := taskMap[n.artifact.Key()]
			if !ok {
				return
			}
			t.SetState(taskui.Failed)
			log.From(log.WithTarget(ctx, t.Log), log.Engine).Error("skipped: %s", reason)
		},
	}

	results, err := e.run(ctx, hooks)

	if interactive {
		overview.Hide()
	} else {
		viewer.Stop()
	}

	fmt.Fprintf(os.Stderr, "\n")
	tracker.PrintPlain()

	f, ferr := openLogsOutput(logsOutput)
	if ferr == nil {
		if serErr := tracker.Serialize(f); serErr == nil {
			f.Close()
			total := len(tracker.Tasks())
			fmt.Fprintf(os.Stderr, "\nCompleted %d tasks. Logs written to: %s. Investigate logs with:\n%s build --view-logs %s\n", total, f.Name(), os.Args[0], f.Name())
		} else {
			f.Close()
		}
	}

	return results, err
}

// openLogsOutput opens the destination for the build-logs JSON snapshot. An
// explicit path is created/truncated; an empty path falls back to a tempfile.
func openLogsOutput(path string) (*os.File, error) {
	if path == "" {
		return os.CreateTemp("", "glbx-build-*.json")
	}
	return os.Create(path)
}

// run is the shared scheduling loop. It builds the graph (idempotent), spins
// up workers up to e.workers parallelism, and observes node transitions via
// hooks. Both Run and RunWithUI delegate here.
func (e *Engine) run(ctx context.Context, hooks runHooks) ([]BuildResult, error) {
	if err := e.graph.Build(); err != nil {
		return nil, err
	}
	if len(e.graph.nodes) == 0 {
		return nil, nil
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, e.workers)
	remaining := len(e.graph.nodes)
	done := make(chan struct{})

	// skipNode recursively marks a node and all its transitive rdeps as skipped.
	// Must be called with mu held.
	var skipNode func(n *node, reason string)
	skipNode = func(n *node, reason string) {
		if n.state != statePending {
			return
		}
		n.state = stateSkipped
		n.err = fmt.Errorf("%s", reason)
		remaining--
		e.results = append(e.results, BuildResult{
			Artifact: n.artifact,
			Err:      n.err,
		})
		if hooks.nodeSkipped != nil {
			hooks.nodeSkipped(n, reason)
		}
		for _, rdep := range n.rdeps {
			skipNode(rdep, fmt.Sprintf("dependency %s was skipped", n.artifact))
		}
	}

	var dispatch func(n *node)
	dispatch = func(n *node) {
		wg.Add(1)
		go func(n *node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			nodeCtx := ctx
			if hooks.nodeStarted != nil {
				nodeCtx = hooks.nodeStarted(n)
			}

			result := e.buildNode(nodeCtx, n)

			mu.Lock()
			e.results = append(e.results, result)
			n.state = stateComplete
			if result.Err != nil {
				n.state = stateFailed
				n.err = result.Err
			}
			remaining--

			if hooks.nodeFinished != nil {
				hooks.nodeFinished(n, result)
			}

			if n.state == stateFailed {
				for _, rdep := range n.rdeps {
					skipNode(rdep, fmt.Sprintf("dependency %s failed", n.artifact))
				}
			} else {
				for _, rdep := range n.rdeps {
					rdep.pending--
					if rdep.pending == 0 && rdep.state == statePending {
						dispatch(rdep)
					}
				}
			}

			if remaining <= 0 {
				close(done)
			}
			mu.Unlock()
		}(n)
	}

	for _, n := range e.graph.Roots() {
		dispatch(n)
	}

	<-done
	wg.Wait()

	return e.results, nil
}

func (e *Engine) buildNode(ctx context.Context, n *node) BuildResult {
	l := log.From(ctx, log.Engine)

	fail := func(err error) BuildResult {
		l.Error("FAILED: %s: %v", n.artifact, err)
		return BuildResult{Artifact: n.artifact, Err: err}
	}

	identity, err := n.artifact.Identity()
	if err != nil {
		return fail(fmt.Errorf("compute identity: %w", err))
	}

	// Cache-hit check. loadManifest goes through Store.MapGet, so with a
	// remote configured a local miss falls through to the registry (pulling
	// the manifest + leaves) rather than triggering a rebuild. A miss with no
	// remote (or a genuine remote miss) returns an error and we build.
	if outputs, err := e.loadManifest(identity); err == nil {
		l.Info("cache hit: %s (id=%s)", n.artifact, identity.Short())
		e.graph.mu.Lock()
		n.outputs = outputs
		e.graph.mu.Unlock()
		return BuildResult{Artifact: n.artifact, Cached: true}
	}

	l.Info("building: %s (id=%s)", n.artifact, identity.Short())

	resolvedInputs, err := e.resolveInputs(n)
	if err != nil {
		return fail(fmt.Errorf("resolve inputs: %w", err))
	}

	bctx := BuildContext{
		Ctx:    ctx,
		Store:  e.store,
		Inputs: resolvedInputs,
	}

	outputs, err := n.artifact.Build(bctx)
	if err != nil {
		return fail(err)
	}

	if err := e.storeManifest(identity, outputs); err != nil {
		return fail(fmt.Errorf("store manifest: %w", err))
	}

	l.Info("complete: %s (%d outputs)", n.artifact, len(outputs))

	e.graph.mu.Lock()
	n.outputs = outputs
	e.graph.mu.Unlock()

	return BuildResult{Artifact: n.artifact}
}

// resolveInputs maps each Input.Name to the corresponding blob hash from the
// dependency's outputs by exact name match.
func (e *Engine) resolveInputs(n *node) (map[string]objstore.Hash, error) {
	inputs := n.artifact.Inputs()
	if len(inputs) == 0 {
		return nil, nil
	}

	resolved := make(map[string]objstore.Hash, len(inputs))

	for _, input := range inputs {
		sourceNode, ok := e.graph.nodeMap[input.Source.Key()]
		if !ok {
			return nil, fmt.Errorf("input source %s not in graph", input.Source.Key())
		}

		found := false
		for _, out := range sourceNode.outputs {
			if out.Name == input.Name {
				resolved[input.Name] = out.Hash
				found = true
				break
			}
		}

		if !found {
			names := make([]string, 0, len(sourceNode.outputs))
			for _, o := range sourceNode.outputs {
				names = append(names, o.Name)
			}
			return nil, fmt.Errorf("output %q not found in %s (available: %v)", input.Name, input.Source.Key(), names)
		}
	}

	return resolved, nil
}
