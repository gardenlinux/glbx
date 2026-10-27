# The build engine

## `internal/artifact`

The engine knows nothing about packages or the sandbox: it talks only to the
uniform `Artifact` interface and the object store.

An artifact has an identity, a label, typed edges — **built-from** (`Depends`,
which impose build order) and **carried-along** (`Includes`, closure-only) —
named inputs, and an opaque build action. The graph wires edges with the
closure rule for carried-along edges and detects cycles over built-from edges
only, so co-emitted siblings that reference each other are legal.

Traversal folds identities from the leaves up, looks each up in the store, and
on a miss resolves inputs, runs the build action, and stores the result
manifest under the identity — with a bounded-parallel scheduler and failure
propagation to dependents. A thin observation seam wires the progress UI to the
engine (one task per artifact) with no UI type visible to the engine's core and
no engine type visible to the UI.

Scoping the engine to a single node restricts the build to one target Key and,
in require-cache mode, forbids building anything else: every non-target node
must resolve from the (pull-through) cache, and a miss becomes a hard error
whose dependents are skipped. This builds exactly the target, pulling all of its
inputs from the cache; a dependency the graph says must exist but that has not
been published skips the target, so the outcome turns on the target's own result
and unrelated nodes left unresolved do not count against it.

The progress UI captures each node's logs into its own task buffer, while the
engine's own scheduling narrative — building, cache hit, complete — goes to a
separate per-run buffer so that chatter is never folded into a node's log. A
streaming mode reuses the per-node buffer and its log printer — the one the
interactive view attaches when a task is entered — but without any UI: it
forwards the single target's logs straight to the console for the whole run, so a
non-interactive runner shows the build as it happens rather than a status summary
after the fact.

After the run each node's buffer is stored as a cache blob keyed by a sibling of
the node's identity, so a later invocation over the same tree can replay the real
build log — and on a cache hit the engine repopulates the node's buffer from that
blob rather than a synthetic hit line. The per-run engine buffer is stored too,
and a run record — keyed by a random run id, referencing every log by blob hash
rather than by its map key — ties them together so a past run stays viewable even
after a rebuild re-points a node's log entry. When a build fails, the engine
buffer's failure lines are flushed to the console after the UI tears down, so the
reason surfaces even though that buffer is otherwise reachable only on request.

A mock artifact drives the whole traversal, cache, and scheduler in tests,
proving the engine is indifferent to what an artifact builds.
