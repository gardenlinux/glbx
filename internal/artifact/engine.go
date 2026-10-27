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

	// target, when non-empty, names the one node Key the engine is allowed to
	// build. noRecurse requires every other node to resolve from cache: a miss
	// on a non-target node is a hard error rather than a build. Together they
	// make the engine build exactly one node, pulling all its inputs from the
	// (pull-through) cache.
	target    string
	noRecurse bool

	// stream forwards the target node's logs live to the console instead of
	// driving the task UI. Only meaningful alongside a single target.
	stream bool

	// engineLog collects the engine's per-run scheduling narrative (building,
	// cache hit, complete) and a copy of each failure, kept separate from the
	// per-node log buffers so that chatter is never cached into a node's log
	// blob. engineLogr is a logger bound to it. Both are set per run by
	// RunWithUI and stay nil in the plain Run path, where engine lines drop.
	engineLog  *log.BufferTarget
	engineLogr *log.Logger
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

// SetScope restricts the engine to building a single node. target is the Key of
// the node to build; when noRecurse is true, every other node must already
// resolve from the cache and a miss is a hard error. A node whose required
// cache input is missing fails, and its dependents are skipped.
func (e *Engine) SetScope(target string, noRecurse bool) {
	e.target = target
	e.noRecurse = noRecurse
}

// SetStream makes RunWithUI forward the target node's logs live to the console
// rather than drive the interactive overview or the non-interactive dot viewer.
// It reuses the per-task log buffer the UI already fills: a single log printer
// is attached to the target's task for the whole run. Intended for a
// single-target no-recurse build, where only the target does real work.
func (e *Engine) SetStream(stream bool) {
	e.stream = stream
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
// Each node's log is stored as a cache blob keyed by its identity, the engine's
// scheduling narrative is stored as a per-run blob, and both are tied together by
// a run record keyed by a random run id. The id is printed so the logs can be
// reviewed afterward with `glbx build --view-logs --run <id>`.
func (e *Engine) RunWithUI(ctx context.Context) ([]BuildResult, error) {
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

	e.engineLog = log.NewBufferTarget()
	e.engineLogr = log.From(log.WithTarget(context.Background(), e.engineLog), log.Engine)

	interactive := term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
	var overview *taskui.TaskOverview
	var viewer *taskui.NonInteractiveViewer
	var streamPrinter *log.LogPrinter

	switch {
	case e.stream:
		// Forward one target's logs live, reusing the same per-task buffer and
		// log printer the interactive overview attaches on entry — no UI, just
		// the forwarder. The target's task.Log fills via the nodeStarted hook.
		if t, ok := taskMap[e.target]; ok {
			streamPrinter = log.NewLogPrinter(t.Log)
			streamPrinter.Run()
		}
	case interactive:
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
	default:
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

	switch {
	case e.stream:
		if streamPrinter != nil {
			streamPrinter.Stop()
		}
	case interactive:
		overview.Hide()
	default:
		viewer.Stop()
	}

	buildFailed := false
	for i := range results {
		if results[i].Err != nil {
			buildFailed = true
			break
		}
	}

	// With the UI torn down, flush the engine buffer's failure lines to the
	// console so a failed build reports why even though the engine buffer is
	// never shown in the UI. Only ERROR records print; the scheduling chatter
	// stays in the buffer, reachable via --view-logs --run <id> --engine-logs.
	if buildFailed && e.engineLog != nil {
		con := log.NewStderrConsoleTarget()
		reader := e.engineLog.Reader()
		for {
			rec, rerr := reader.Read()
			if rerr == log.ErrNoMore {
				break
			}
			if rec.Level == log.Error {
				con.Emit(rec)
			}
		}
	}

	// Persist every node's log, the engine log, and a run record tying them
	// together, then point the user at the run id. Streaming prints only the
	// one-line hint; everything else prints the status table too.
	runID := newRunID()
	if err := e.storeRun(runID, tracker); err != nil {
		log.From(ctx, log.Engine).Warn("record build run: %v", err)
	} else if e.stream {
		fmt.Fprintf(os.Stderr, "\nInvestigate logs with:\n%s build --view-logs --run %s\n", os.Args[0], runID)
	} else {
		fmt.Fprintf(os.Stderr, "\n")
		tracker.PrintPlain()
		fmt.Fprintf(os.Stderr, "\nCompleted %d tasks. Investigate logs with:\n%s build --view-logs --run %s\n", len(tracker.Tasks()), os.Args[0], runID)
	}

	return results, err
}

// storeRun writes each node's log blob (keyed by identity) and the per-run
// engine-log blob, then a run record keyed by the run id whose per-node entries
// point at the log blobs by hash. Nodes with no built identity contribute a
// stateful entry without a log.
func (e *Engine) storeRun(runID string, tracker *taskui.TaskTracker) error {
	engineLogHash, err := storeLogBlob(e.store, e.engineLog)
	if err != nil {
		return fmt.Errorf("store engine log: %w", err)
	}

	rec := &RunRecord{ID: runID, EngineLog: engineLogHash}
	for _, t := range tracker.Tasks() {
		rn := RunNode{Key: t.Name, State: t.State(), Start: t.Start(), End: t.End()}
		if a := e.graph.Find(t.Name); a != nil {
			if identity, ierr := a.Identity(); ierr == nil {
				if h, serr := storeLog(e.store, identity, t.Log); serr == nil {
					rn.Log = h
				}
			}
		}
		rec.Nodes = append(rec.Nodes, rn)
	}
	return storeRunRecord(e.store, rec)
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
			skipNode(rdep, fmt.Sprintf("dependency %s was skipped", n.artifact.Key()))
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
					skipNode(rdep, fmt.Sprintf("dependency %s failed", n.artifact.Key()))
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

// elog returns the logger for the per-run engine buffer, or a no-op logger when
// no engine buffer is set (the plain Run path). It lets buildNode route the
// scheduling narrative to the engine buffer without a nil check at each site.
func (e *Engine) elog() *log.Logger {
	if e.engineLogr != nil {
		return e.engineLogr
	}
	return log.From(nil, log.Engine)
}

func (e *Engine) buildNode(ctx context.Context, n *node) BuildResult {
	l := log.From(ctx, log.Engine)
	eng := e.elog()

	// FAILED lands in both the node buffer — where it is the artifact's own
	// failure detail, shown when the node is selected — and the engine buffer,
	// whose ERROR records are flushed to the console when the build fails.
	fail := func(err error) BuildResult {
		l.Error("FAILED: %s: %v", n.artifact.Key(), err)
		eng.Error("FAILED: %s: %v", n.artifact.Key(), err)
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
		eng.Info("cache hit: %s (id=%s)", n.artifact.Key(), identity.Short())
		// Repopulate the node buffer with the real build log recorded when the
		// node was first built, rather than a synthetic cache-hit line. A miss
		// (no stored log for this identity) leaves the buffer empty.
		if buf, ok := log.BufferFrom(ctx); ok {
			_ = loadLogInto(e.store, identity, buf)
		}
		e.graph.mu.Lock()
		n.outputs = outputs
		e.graph.mu.Unlock()
		return BuildResult{Artifact: n.artifact, Cached: true}
	}

	// In no-recurse mode only the target node may be built; every other node
	// must have resolved from the cache above. A miss here means a dependency
	// the graph says must already exist was not published — a build-ordering
	// error, surfaced loudly rather than silently rebuilt.
	if e.noRecurse && n.artifact.Key() != e.target {
		return fail(fmt.Errorf("required cache input missing (no-recurse): %s (id=%s)", n.artifact.Key(), identity.Short()))
	}

	eng.Info("building: %s (id=%s)", n.artifact.Key(), identity.Short())

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

	eng.Info("complete: %s (%d outputs)", n.artifact.Key(), len(outputs))

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
