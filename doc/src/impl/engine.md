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
inputs from the cache and failing loudly if an input the graph says must exist
has not been published.

The progress UI captures each node's logs into its own task buffer. A streaming
mode reuses that same buffer and its log printer — the one the interactive view
attaches when a task is entered — but without any UI: it forwards the single
target's logs straight to the console for the whole run, so a non-interactive
runner shows the build as it happens rather than a status summary after the fact.

A mock artifact drives the whole traversal, cache, and scheduler in tests,
proving the engine is indifferent to what an artifact builds.
