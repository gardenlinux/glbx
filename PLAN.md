# Implementation Plan

This plan sequences the construction of glbx from the component designs in this
repository into a concrete, buildable order. It is a plan of **what to build when**,
not of how each piece works internally — the component documents own the *how*.

The sequence exists to satisfy one discipline: **every phase stands on its own.**
Each phase produces something that compiles, that is covered by tests or a direct
command-line entry point, and that depends only on phases already completed. No
phase leaves behind code that is meaningful only once a later phase arrives.
Interfaces and helpers may be introduced ahead of their first real consumer, but
only when they are independently exercisable — by a unit test, a fake, or a demo —
at the moment they land.

Each phase is a small, coherent unit. The bullets within a phase are its steps.

## How the plan is grouped

The work falls into seven groups, built in order:

- **A — Foundations.** Project scaffold and the pure, dependency-free primitives
  everything else rests on: content identity, the object store, logging, and the
  progress UI.
- **B — Debian format layer.** The pure parsers and the dependency resolver that
  turn Debian metadata into typed values. No I/O.
- **C — Repository access, import, and lock.** Fetching from an APT archive,
  importing a source package into the working tree, and pinning build-time
  tooling. The front half of the pipeline — usable before any build engine exists.
- **D — Execution environment.** The hermetic sandbox, built directly on kernel
  namespaces, as a self-contained runtime with its own command-line entry point.
- **E — The build engine.** The artifact graph and its traversal, developed and
  tested against a mock artifact, independent of what a real artifact builds.
- **F — Package build and image assembly.** The concrete artifact kinds — source
  build, binary-package validation, image — that compose the engine, the sandbox,
  the Debian layer, and the store into the real pipeline.
- **G — Completion.** The remaining command surface, a staging-preparation helper,
  the end-to-end acceptance test, and the documentation build.

The dependency structure is a DAG, not a chain, so some groups could be reordered.
The chosen order keeps each phase close to the first thing that uses it while never
building ahead of what can be tested.

---

## Group A — Foundations

### Phase A0 — Project scaffold
- Introduce the Go module (`github.com/gardenlinux/glbx`), a `Makefile` as the
  single orchestration entry point (format, vet, build, test targets), and a
  `.gitignore`.
- Establish the source layout: `cmd/` for binaries, `internal/` for packages.
- Move the existing design documents under a `doc/` directory so the repository
  root is reserved for code and build tooling.
- *Exercisable by:* the module builds and vets clean.

### Phase A1 — Content identity
- Implement the hash type: a validated 256-bit digest as lower-case hex, with
  equality, sharding prefix/suffix, and nothing else (opaque token).
- Implement the ordered-tuple fold (`ConcatHash`) that composes a single hash
  from framed parts, per the identity design.
- Implement recursive directory hashing (dirhash): fixed-order, content-only,
  one executable bit, recursive per subtree.
- *Exercisable by:* determinism unit tests — reorderings and permission noise do
  not change a dirhash; the fold is stable and framed; identical inputs match.

### Phase A2 — The object store
- Implement the content-addressed blob store: atomic temp-then-rename writes,
  hash-on-write verification, `has`/`open`/`path`/`store`/`delete`/`iterate`.
- Implement the identity→manifest map store and the manifest byte format
  (`<hash> <name>` lines) as the one source of truth for manifest serialization.
- Define the store **interface** and the pull-through composition: a store built
  from a local store and an injected remote, where a local miss is filled from the
  remote and re-read locally. Ship the local backend as the only implementation of
  the remote seam for now; the interface exists so the backend can be swapped later.
- Implement keep-set garbage collection: sweep blobs against a caller-supplied set,
  then sweep the map to follow the surviving blobs.
- *Exercisable by:* blob/map CRUD and GC unit tests on a temp directory; a
  pull-through test driven by an in-process fake remote.

### Phase A3 — Structured logging
- Implement leveled, component-tagged records; the target (sink) abstraction;
  a console sink with TTY-aware coloring; an in-memory buffer sink with a
  tailing reader; a replay printer; JSON serialization of a buffer; and the
  subprocess-output-to-records helper.
- Carry the logging target on the context so any code can obtain a component
  logger without threading a logger through every call.
- *Exercisable by:* pure unit tests (levels, buffering, serialization round-trip).

### Phase A4 — Progress UI
- Implement the task tracker: a flat set of named tasks, each with a lifecycle
  state, timestamps, and its own log buffer.
- Implement the views: an interactive live overview (raw-mode TTY, arrow
  navigation, enter-to-tail a task's logs via an injected callback), a
  non-interactive status ticker for non-TTY output, a one-shot plain dump, and a
  Gantt rendering.
- Implement whole-tracker serialization so a finished run's per-task logs can be
  saved and replayed later.
- Add a standalone demo binary that drives the full UI surface with synthetic
  tasks — the UI's end-to-end exercise, with no engine behind it.
- *Exercisable by:* the demo binary plus pure unit tests for the tracker, states,
  Gantt, and serialization.

---

## Group B — Debian format layer

### Phase B1 — Version ordering
- Implement Debian version parsing (`epoch:upstream-revision`), canonical
  formatting, and `deb-version(7)` comparison (tilde ordering, alternating
  non-digit/digit segments), plus constraint checking against an operator.
- *Exercisable by:* comparison tests against known dpkg orderings.

### Phase B2 — Stanza parsing
- Implement the deb822 stream parser: lower-cased field names, continuation
  lines, comments, blank-line stanza separation, over a supplied reader.
- *Exercisable by:* parser edge-case unit tests.

### Phase B3 — Dependency relationships
- Implement the dependency-relationship parser: version constraints, architecture
  qualifiers and restriction lists, build-profile groups, alternatives, and clause
  lists.
- Implement the matching helpers: architecture-wildcard matching and
  profile-activation filtering.
- *Exercisable by:* full-syntax parse tests and arch/profile filter tests.

### Phase B4 — The package index
- Implement the in-memory binary-package index built from a Packages stream:
  pre-parsed dependency fields, lookup by real name and by virtual name
  (Provides), essential-set and merge/subset queries.
- *Exercisable by:* load/lookup/merge/essentials tests over fixture stanzas.

### Phase B5 — The dependency resolver
- Implement the backtracking resolver: propagation with a decision stack and
  state snapshots, virtual packages via providers, conflicts via exclusion,
  ranked alternatives, deterministic sorted output, and a structured error tree
  on failure.
- *Exercisable by:* unit tests over in-memory indexes — chains, diamonds,
  virtuals, conflicts with backtracking, alternatives, version constraints,
  and unsatisfiable inputs.

### Phase B6 — Build configuration schemas
- Implement host-architecture detection.
- Implement the typed loaders for the on-disk declarative files: the per-package
  build declaration (`build.yml`), the source pins (`sources.yml`), the image
  package list, and the pinned-tooling files. Introduce only the fields that
  later phases will actually consume as they are needed; the loader may grow.
- *Exercisable by:* loader unit tests over temp-directory fixtures.

---

## Group C — Repository access, import, and lock

### Phase C1 — Stream primitives
- Implement the pure, host-safe byte helpers: streaming SHA-256 readers/writers,
  cleartext-signature envelope extraction, and `.deb` ar-archive member
  extraction in-process.
- Implement the subprocess-backed helpers that only transform byte streams:
  decompressors (gzip/xz/bzip2/zstd with extension dispatch), tar
  extract/create/list with deterministic creation flags, and GPG verification.
- *Exercisable by:* unit tests per codec and a tar-determinism test (host
  compression and `gpgv`/`tar` tools present).

### Phase C2 — APT repository access
- Implement release-metadata access: fetch and verify a signed release index
  (GPG, or cleartext-strip when verification is disabled), with an object-store
  cache keyed by the fetch coordinates; parse the release's path→hash map and
  its date.
- *Exercisable by:* unit tests over a loopback HTTP server with verification
  disabled, plus pure release-parse tests.

### Phase C3 — Source import
- Implement importing one source package: resolve the highest version from the
  signed sources index, download and hash-verify each source file into the store,
  and extract the packaging into the working tree.
- Handle the two committed source formats and the normalization of the legacy
  format into a quilt-shaped tree (deterministic synthesized patch, timestamps
  stripped), writing the source pin file that records each archive by hash and
  retrieval locations.
- Introduce the `glbx` binary with its first subcommand, `import`, and the
  hand-rolled subcommand dispatch it will grow within.
- *Exercisable by:* loopback-HTTP and fixture tests for each format; a
  network-gated test against a real archive, guarded so it skips when offline.

### Phase C4 — Build-tooling lock
- Implement lock generation for one package: fetch and verify the binary index,
  extract the package's declared build dependencies, resolve the closure, fetch
  and hash-verify every resolved `.deb` into the store, and write the explicit
  per-architecture pinned-tooling file (each entry: name, exact version, and per
  file its architecture, hash, and retrieval locations).
- Implement the parallel, verified `.deb` fetch used by the lock step.
- Add the `lockfile` subcommand.
- *Exercisable by:* end-to-end lock tests against a fake archive served over
  loopback HTTP; deterministic-serialization tests.

---

## Group D — Execution environment

### Phase D1 — The control channel
- Implement the local message-boundary socket transport with file-descriptor
  passing: the request/response framing, the socket-pair constructor, the
  host-side client (one in-flight call, cookie-matched), and the reactive
  server side.
- Implement the environment-resolution rule (overlay vs. reset) the layers share.
- *Exercisable by:* round-trip unit tests over a real socket pair with FD passing,
  disconnect handling, and codec round-trips — all unprivileged.

### Phase D2 — The helper stub and host-direct execution
- Implement the helper binary: a tiny reactive process that serves the control
  channel's request set from inside whatever namespace it is launched into.
- Implement the two host-direct, in-process implementations: an exec-only
  environment (plain fork/exec, the bottom of every stack) and a filesystem view
  over ordinary system calls.
- *Exercisable by:* host-execution unit tests (exit codes, stdio wiring, env, cwd,
  credentials) — unprivileged, no namespaces.

### Phase D3 — The channel-backed environment and ID mapping
- Implement the single channel-driven environment that provides both the
  process-execution and the filesystem-view interfaces by driving one helper
  over the control channel, including FD-returning file opens.
- Implement ID-map computation (intersecting subordinate ranges with the parent
  map, reserving inner root for the calling user) and its application through the
  set-uid id-map helpers.
- *Exercisable by:* pure ID-map computation unit tests; the channel environment
  exercised through the host-direct base beneath it.

### Phase D4 — The namespace stack
- Implement the layered stack as the shared channel-backed environment constructed
  with different unshare flags and credentials: the user-namespace layer, the
  mount-namespace layer (private mount table for staging), and the pivoted run
  layer (PID namespace plus `pivot_root`, with the slave/private mount propagation
  and the base filesystem mounts).
- Add a command-line entry point that unpacks a stored root filesystem and runs a
  command inside the sandbox — the direct way to exercise the whole stack.
- *Exercisable by:* namespace tests against a minimally bootstrapped root
  filesystem (prepared out of band, never committed), gated to skip where
  unprivileged user namespaces are unavailable.

---

## Group E — The build engine

### Phase E1 — The artifact graph and traversal
- Define the uniform artifact: identity, label, typed edges (built-from vs.
  carried-along), named inputs, and an opaque build action; and the manifest as
  an artifact's result.
- Implement the graph: edge wiring with the closure rule for carried-along edges,
  and cycle detection over built-from edges only.
- Implement the traversal: fold identities from the leaves up, look each up in the
  store, and on a miss resolve inputs, run the build action, and store the manifest
  under the identity — with a bounded-parallel scheduler and failure propagation
  to dependents.
- Wire the progress UI to the engine through a thin observation seam (one task per
  artifact; state transitions and per-task log routing), with no UI type visible to
  the engine's core and no engine type visible to the UI.
- *Exercisable by:* graph and engine tests driven by a mock artifact over a real
  temp-directory store — build order, closure edges without false cycles, cache
  hit/miss, idempotence, and parallel failure propagation. The mock is what proves
  the engine is indifferent to what an artifact builds.

---

## Group F — Package build and image assembly

### Phase F1 — Install into a root filesystem
- Implement the resolve-only entry point (a thin wrapper over the resolver) and
  the dpkg-based install of a resolved set into a root filesystem inside the
  sandbox: bind the `.deb` blobs in, unpack-all then configure-pending to break
  the pre-dependency cycles.
- Implement bootstrapping a minimal working root filesystem from an index's
  essential set, as the base the install step runs against.
- *Exercisable by:* pure resolve tests; a bootstrap-and-install integration test
  gated on sandbox availability and (skipping when offline) a real archive.

### Phase F2 — The source-build artifact
- Implement the source-build artifact: identity folded from its source dirhash,
  architecture, and its built-from dependency identities; the built-from edges
  read from the build declaration.
- Implement its build action: assemble the chroot (pinned tooling unpacked in,
  locally built dependencies layered over, source tree and pinned archives staged
  in), apply the deterministic content-derived local version, run the Debian
  source build unprivileged inside the sandbox, and harvest every produced `.deb`
  and its control stanza into the store as the artifact's manifest.
- Extend `build` to drive the engine over a source-build target.
- *Exercisable by:* identity-determinism unit tests; a metadata-only synthetic
  graph test; and a single-package real build (import → lock → build) gated on
  sandbox and network.

### Phase F3 — The binary-package artifact
- Implement the per-binary artifact that does no compiling: a built-from edge to
  its source build and carried-along edges to co-emitted runtime siblings, with
  identity folding the sibling keys without ordering against them.
- Implement its build action as validation of exactly the selected binary: a
  locality check that every runtime alternative is satisfiable from locally built
  binaries, and an install check in a bootstrapped sandbox. Introduce the
  per-binary tolerated-externals allowance here — only now, because this is the
  stage whose validation reality first requires it.
- Re-emit the validated `.deb` as the thing downstream artifacts depend on.
- *Exercisable by:* synthetic locality-rejection tests and an install-check test
  with mock packages; the real single-package path now runs through validation.

### Phase F4 — The image artifact
- Implement the package-set registry that scans the working tree and the graph
  builder that turns an image's named package list into a discovered graph, and
  the lock step for the image's throwaway configuration tooling.
- Implement the image artifact: identity folding every transitive binary
  dependency; a build action that narrows the runtime closure to the shipping set,
  stages the three-layer overlay (payload / throwaway tooling / mutations), runs
  dpkg configuration against the merged view, composes the final view from payload
  and mutations only, and serializes the tree deterministically.
- Extend `build` to drive an image target.
- *Exercisable by:* the full end-to-end build of a real image from a prepared
  working tree, gated on sandbox and network.

---

## Group G — Completion

### Phase G1 — The remaining command surface
- Add the inspection and maintenance commands: graph rendering (including the
  saved-run Gantt), object-store administration (status, keep-set GC, raw
  blob and map access), working-tree auto-discovery, and a status command.
- *Exercisable by:* black-box command tests against the built binary.

### Phase G2 — Staging preparation and acceptance test
- Add a helper that prepares a complete working tree for a fixed package set
  (import each package, place its build declaration, generate each lock) and the
  set of per-package declaration fixtures it uses.
- Add the end-to-end acceptance script that drives the whole pipeline from an
  empty tree to a built image and a smoke-test run inside it.
- *Exercisable by:* the acceptance script itself, run against a real archive.

### Phase G3 — Documentation build
- Restructure the design documents into a navigable book, add the per-component
  implementation pages alongside the concept pages, and wire the book build into
  the orchestration entry point.
- *Exercisable by:* the documentation builds.

---

## Notes on ordering

A few deliberate choices worth stating, so later work does not undo them:

- **The engine is built and proven before any real artifact exists.** It talks
  only to the artifact interface and the store; a mock artifact exercises the whole
  traversal, cache, and scheduler. This keeps the engine permanently free of any
  knowledge of packages or the sandbox.
- **The sandbox is a self-contained runtime with its own entry point,** developed
  and tested against a plain prepared root filesystem before the build artifacts
  consume it. Nothing in the sandbox depends on the artifact graph or the store.
- **The front half of the pipeline (import and lock) is usable before the engine
  or the sandbox exist,** since importing and pinning are repository-and-store
  operations, not builds.
- **Identity and the store are built first and must be correct from the start.**
  An identity or manifest-format error fails silently as a wrong cache hit rather
  than a crash, so these phases carry determinism tests from the moment they land.
- **Pinned-tooling tolerances and other per-binary allowances are introduced only
  at the phase whose validation first needs them,** never speculatively — the
  graph and the schemas grow to meet real requirements, not anticipated ones.
