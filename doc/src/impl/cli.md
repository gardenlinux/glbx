# Command-line interface

This chapter is the field guide to `cmd/`: the `glbx` command layer and the two
auxiliary binaries. It doubles as the command and flag reference. The framing is
how the command layer is wired — each subcommand is a thin shell that parses
flags and drives one internal package — so it is both the place to look up a flag
and the place to start when adding or changing a command.

## Dispatch: `cmd/glbx/main.go` + `commands.go`

Dispatch is hand-rolled, not a CLI framework. `main.go` reads `os.Args[1]` as the
command name, `os.Args[2:]` as its arguments, and switches on the name to a
`cmdXxx(args []string) error` function. The known names are, exactly:

```
import  get-pkg-metadata  check-updates  lockfile  build  graph
cache   restore-cache     publish        resolve    status exec-chroot
```

`help`/`--help`/`-h` print usage; an unknown command logs an error and exits `2`.
A non-nil error from a `cmdXxx` is logged as `glbx <cmd>: <err>` and exits `1`.
There is no global flag parsing — every flag is per-command, so each command owns
its own `flag.FlagSet` (`flag.ExitOnError`, so a parse error exits `2` with the
flagset's usage).

Each command lives in its own `*.go` file next to `main.go`. `commands.go` holds
two cross-command helpers: `repeatedFlag`, a `flag.Value` that accumulates a
repeatable `--params` into a slice, and the small `lockfile`/`status` commands.
The shared conf-dir / store / registry plumbing lives in `util.go` (below).

A command splits into two kinds of work: resolve the environment (where is the
cache, where is the working tree, is a registry configured) via the `util.go`
helpers, then hand a parsed request to an internal package. Keep that shape when
adding a command.

## Shared plumbing: `cmd/glbx/util.go`

Every command that touches the tree or the store routes through these helpers, so
their behaviour is uniform:

- **`findConfDir()`** walks up from the current directory looking for the
  working-tree root: a directory that holds a `pkgs/` subdirectory *and* at least
  one `rootfs-<label>.yml` variant (checked via
  `buildcfg.DiscoverRootfsLabels`). Returns `""` if none is found. Commands take a
  `--conf-dir` flag and fall back to this; a command that still has `""` errors
  with "cannot find configuration directory; use --conf-dir".
- **`openStore(dir)`** opens the object store. It always opens a local store at
  `dir` (or the default root when `dir` is `""`). When `GLBX_REGISTRY` is set it
  wraps that in a pull-through store (`objstore.NewPullThrough(local, registry)`)
  so a cold local cache is filled from the registry on demand. Commands that must
  read *only* the local store — `graph`, `publish`, `resolve`, `exec-chroot` —
  deliberately call `objstore.NewLocal` directly and bypass this.
- **`resolveCacheDir(dir)`** returns the concrete directory a store resolves to
  (`objstore.DefaultRoot()` when `dir` is `""`), purely so a command can log it.
- **`scratchDir(dir)`** is the source-build scratch root: a `tmp/` subdirectory of
  the resolved cache root, so build scratch spills to the cache disk rather than
  RAM.
- **`registryFromEnv()`** reads `GLBX_REGISTRY` (the `host[:port]/repo` reference;
  empty disables the remote) and `GLBX_REGISTRY_INSECURE` (any non-empty value
  selects plaintext HTTP for a local test registry).
- **`rootContext(c)` / `rootContextStderr(c)`** build a `context.Context` carrying
  a console log target plus a logger for component `c`. The `Stderr` variant
  routes every log level to stderr, used by commands whose stdout carries
  machine-parseable output (`import --no-merge`, `check-updates`, `resolve`).

### Environment variables

| Variable | Read by | Meaning |
| --- | --- | --- |
| `GLBX_CACHE` | `objstore.DefaultRoot`, `exec-chroot` | object-store root; default is `~/.cache/glbx` |
| `GLBX_REGISTRY` | `util.registryFromEnv` | remote registry `host[:port]/repo`; enables the pull-through / registry reads |
| `GLBX_REGISTRY_INSECURE` | `util.registryFromEnv` | non-empty ⇒ plaintext HTTP (local test registry) |
| `GLBX_REGISTRY_TOKEN` / `GLBX_REGISTRY_USER` | `objstore/remote.go` | bearer-challenge credentials for an authenticating registry |
| `GLBX_ID_SEED_OVERRIDE` | `artifact.IDSeed` | replaces the global identity seed, giving an independent cache namespace without editing source |
| `GLBX_STUB_PATH` | `exec-chroot` | `exec_env_stub` path when `--stub`/default lookup is not used |

The `--cache`, `--conf-dir`, `--arch`, and `--stub` flags recur across commands;
all auto-discover or default (`--arch` defaults to `buildcfg.HostArch()`), so a
command run from inside a working tree with a default cache needs none of them.

## Commands

### `import` — `import.go`

```console
$ glbx import debian:hello
```

Imports an upstream source package into the working tree through the namespace's
importer (`provider.LookupImporter`). The target is always
`<namespace>:<package>` with an explicit namespace: `import` *creates*
`pkgs/<name>/`, so there is no `build.yml` to dispatch on. A bare namespace with
no package is an error.

- `--params k=v,k=v` (repeatable) carries provider parameters (repo, dist,
  snapshot, keyring, no-verify, …); `--params-help <namespace>` prints what a
  namespace accepts and exits.
- `--update-tag <tag>` records the auto-update source in the import commit
  (defaults to `debian:<dist>`); `check-updates` later filters on it.
- `--no-git-history` writes `pkgs/<package>/` directly instead of recording a
  commit.
- `--no-merge` builds the import commit and prints its result as a single JSON
  object (`importJSON`: `pkg`, `version`, `commit`, `first_import`,
  `already_present`) on stdout, leaving the working tree untouched. Logs go to
  stderr in this mode. It requires git-history mode (it reports a commit to
  merge), so `--no-merge --no-git-history` together is an error.
- `--output <dir>` is the working-tree root to import into (default `.`).

Default behaviour records the import as a commit on the package's upstream
lineage, then merges it into the current branch by **replacing the process with
`git merge --no-edit`** (`syscall.Exec`), so the merge's exit status and output —
including an unresolved conflict — propagate verbatim. A first import of a package
(an unrelated lineage) adds `--allow-unrelated-histories`. The git-lineage
mechanics live in `internal/importer/git.go`.

### `get-pkg-metadata` — `get_pkg_metadata.go`

Prints the most recent import metadata for each package on its upstream lineage
reachable from `HEAD`, in topological order, filtered to packages present under
`pkgs/`. Drives `importer.CollectPkgMetadata`. `--format yaml|json` (default
`yaml`); `--conf-dir` with the usual `findConfDir` fallback. An empty result
marshals as `[]`, not `null`.

### `check-updates` — `check_updates.go`

```console
$ glbx check-updates
hello 2.10-3 -> 2.10-4
```

Reports packages whose newest version in the configured repo/dist is higher than
the version pinned on their lineage. With no positional arguments it checks every
present package; positional package names restrict the set. Drives
`importer.CheckUpdates`. Logs to stderr, results to stdout.

Flags: `--repo` (default `https://deb.debian.org/debian`), `--dist` (default
`testing`), `--keyring` (default the Debian archive keyring), `--no-verify` (skip
GPG verification), `--cookie` (reuse a cached `InRelease` within a session),
`--update-tag` (only consider packages whose recorded `auto_update` equals this
tag), `--cache`, `--conf-dir`, and `--format text|json` (default `text`; `text`
prints `pkg old -> new` lines, `json` an array of `importer.Update`).

### `lockfile` — `commands.go`

Generates a pinned dependency lock through a provider selected by the target
namespace (`provider.LookupLockGenerator` + `provider.ParseTarget`). The target is
`<namespace>:<arg>`, or a bare namespace for a zero-argument generator:

| Target | Lock generated |
| --- | --- |
| `pkg:<name>` | dispatch on the package's `build.yml` `pkg_type` (recommended) |
| `tooling-pkg:<name>` | build-tooling lock for an `images.d/tooling/` package |
| `rootfs` | the rootfs configuration-tooling lock (zero-arg) |
| `image-type:<name>` | an image type's assembly-tooling lock (→ `tooling-deps.yml`) |
| `debian:<name>` | the plain Debian lock, directly |
| `debian-kernel:<name>` | the kernel lock, directly |

`--params`/`--params-help <ns>` as for `import`. `--output <dir>` is the
working-tree root (default `.`). `--stub` supplies the `exec_env_stub` path, which
only a container-backed generator (`debian-kernel`) needs. `--cache` as usual.
`lockfile` has **no** `--arch` flag: the target architecture is a provider
parameter (`--params arch=<arch>`, default the host arch). One invocation locks
one architecture and the writer merges it into any existing lock, so a
multi-architecture lockfile is produced by running the command once per
architecture (see [package-build](../concepts/package-build.md#one-run-locks-one-architecture-and-merges)).

### `build` — `build.go`, `reproducible.go`

The default build (no mode flag) builds the whole graph with the interactive task
UI: `build.BuildGraph` assembles the artifact graph, `artifact.NewEngine` runs it
bounded by `--jobs`, and `engine.RunWithUI` drives the live view. On completion it
prints each rootfs variant's and each image's identity (the handle `exec-chroot`
and `publish` consume) and fails if any node failed.

`--jobs` defaults to `floor(sqrt(NumCPU))` (and is clamped back to that default if
given `<= 0`). `--arch`, `--cache`, `--conf-dir`, `--stub` are the standard
plumbing flags.

Scoping and query flags (mutually independent modes, mostly short-circuiting):

- `--target <Key>` builds only the node with that Key (e.g. `rootfs:base:amd64`,
  `image:base-ext4:amd64`, or a package key). A targeted build's success is
  decided by the target's own result alone — the engine still walks the graph, so
  unrelated nodes that cannot resolve under `--no-recurse` report errors that are
  not this job's concern, but a genuinely missing dependency still fails the
  target (the engine skips a node whose dependency was absent).
- `--no-recurse` requires every non-target node to resolve from cache; a miss is a
  hard error. Requires `--target`.
- `--stream` forwards the target's logs live to the console instead of the task
  UI. Requires `--target` and `--no-recurse`.
- `--print-id <Key>` prints the target's identity (content-addressed map key) and
  exits without building or mutating the cache.
- `--invalidate <Key>` deletes the map entry for the target's identity and exits
  (a no-op if absent).
- `--list-outputs <Key>` prints an already-built target's output names (via
  `artifact.ResolveOutputs`).
- `--output-path <Key> <output>` prints the cache blob path of one named output
  of an already-built target; the output name is a positional argument.

```console
$ glbx build --target rootfs:base:amd64 --no-recurse --stream
```

**Reproducibility — `--check-reproducible`** (dispatched before flag parsing in
`cmdBuild`, implemented in `reproducible.go`). This is a *flag*, not a top-level
command. It rebuilds already-built targets and checks that each reproduces its
cached output. The run is flat: every node rebuilds from its dependencies'
*original* cached outputs, bounded by `--jobs`, so a non-reproducible dependency
never cascades into its dependents. It honours `--target` (one node) or checks
every node otherwise, and **requires `--report <file>`** — the JSON report
(`reproReport`: per-node `match`/`mismatch`/`error`, original and reproduced
manifest hashes, and the rebuild's log blob hash) is the only reference to the
rebuild logs, since no map entry points at a reproducibility log. The live UI
shows green (reproduced) or red (mismatch or rebuild failure).

**Log review — `--view-logs`** (also dispatched before flag parsing). Reviews
stored build logs instead of building. Three sources, mutually exclusive:

- no `--run`/`--report`: the current tree. Each node's state comes from whether
  its output manifest exists (built → green, log-but-no-manifest → failed, neither
  → pending); logs load from the cache by identity.
- `--run <id>`: a recorded run (`artifact.LoadRunRecord`).
- `--report <file>`: a reproducibility report (match → green, mismatch/error →
  red), logs loaded by the blob hash the report records.

A positional `<target>` prints that one node's log and exits. `--engine-logs`
(only valid with `--run`) prints the run's engine scheduling narrative — the only
way to surface it. With a TTY the viewer is the interactive overview (select a
node, Enter to print its log, `q` to return); non-interactively it prints the
plain node list.

### `graph` — `graph.go`

Exports the dependency graph. Default format is Mermaid (wrapped in a fenced block
with a node count); `--format=json` emits the deterministic `graphExport` — nodes
in stable topological order (each with `key` and `shortName`) and
deterministically-sorted edges, structure only, so it changes when the graph shape
changes rather than on every source edit. `--check-built` (JSON only) adds a
`builtStatus` map keyed by node Key: `true` where the node's identity is present
in `$GLBX_REGISTRY`. It probes the registry directly (not the pull-through), so it
is a pure set of existence checks that fetches nothing; with no registry
configured every node is reported not-built.

`--gantt <run id>` short-circuits the graph build and renders a Mermaid gantt chart
of a recorded run's timeline (`artifact.TrackerFromRun`). `--output <file>` writes
to a file instead of stdout; `--arch`, `--cache`, `--conf-dir`, `--stub` as usual.
`graph` reads only the local store.

### `cache` — `cache.go`

Object-store administration, dispatched on a subcommand:

- **`status`** — counts blobs and map entries, prints the cache root.
- **`gc [--dry-run]`** — computes the keep-set from the current checkout's build
  graph (every built, reachable artifact's manifest and leaf blobs via
  `graphResult.BuiltOutputs`) and reclaims everything else. The store is a pure
  cache — a reclaimed output rebuilds, a reclaimed input re-fetches — so graph
  reachability is the entire policy. With no conf-dir the keep-set is empty (a
  warning, everything collectible). `--dry-run` reports the count without
  deleting. Takes `--arch`, `--conf-dir`, `--stub`.
- **`blobs <get|store|list|delete|check|path>`** — raw content-addressed blob
  access (`store` ingests a file and prints its hash; `get` streams a blob to
  stdout; `path` prints its on-disk path; etc.).
- **`map <get|set|list|delete|check>`** — raw identity→manifest map access.

These all operate on `openStore("")` (the default root, pull-through if a registry
is set).

### `restore-cache` — `restore_cache.go`

Fills a cold cache from recorded pins: drives `restore.Restore` over the conf-dir
graph, fetching the source archives and build-tooling `.debs` the build will need.
Flags are just the plumbing (`--cache`, `--conf-dir`, `--arch`). It is the fetch
half of the warm-cache path that `publish` then mirrors.

### `publish` — `publish.go`

Mirrors a checkout's locally-held content to an OCI registry, reading the local
store directly (never through the registry it is filling) and uploading only the
blobs and aliases the registry lacks — so it is idempotent on re-run. The worklist
is the union of the recorded inputs (`restore.InputHashes`) and the built outputs
(`graphResult.BuiltOutputs`), deduplicated. `--target <Key>` scopes to a single
built node's manifest, output blobs, and identity alias (its inputs were published
by the jobs that built its dependencies). The registry comes from `--registry` or
`$GLBX_REGISTRY`; `--arch`, `--cache`, `--conf-dir`, `--stub` as usual.
Per-item progress is logged `[done/total] outcome kind ref`.

### `resolve` — `resolve.go`

Resolves package names against a binary `Packages` index using
`internal/resolver`, for inspection and debugging. The index comes from
`--index <blob-hash>` (a stored index) or, failing that, is fetched from
`--repo`/`--dist`/`--arch` (`--cookie` reuses a cached `InRelease`). The
pseudo-root `?essential` expands to every `Essential: yes` package in the index,
as if each were named on the command line. Logs go to stderr; the resolved package
list goes to stdout (`name version`, or `source:name version` with `--source`).
Reads only the local store.

```console
$ glbx resolve ?essential bash
```

### `status` — `commands.go`

A one-shot diagnostic: opens the default store and prints the cache root and the
discovered working-tree root (or that none was found from the current directory).

### `exec-chroot` — `exec_chroot.go`

```console
$ glbx exec-chroot <rootfs-hash> /bin/bash
```

Unpacks a stored rootfs and runs a command inside the full sandbox stack — the
interactive way to inspect a built rootfs. Given a rootfs identity hash, it looks
the hash up in the map, reads the manifest to find the `rootfs.tar.gz` blob,
stands up a `container.Stack`, mounts a tmpfs inside its mount namespace, extracts
the rootfs onto it, then creates a container over the extracted tree and execs the
command, exiting with the command's exit code. `--cache` defaults to `$GLBX_CACHE`
and the stub path to `$GLBX_STUB_PATH` (then `container.StubPath()`). `--explore`
skips container creation and execs directly inside the mount namespace (with
`$GLBX_ROOTFS` set to the extracted tree) for lower-level poking. The sandbox
internals are the subject of [the execution-environment chapter](./exec-env.md).

## Auxiliary binaries

### `cmd/exec_env_stub`

The privileged-side sandbox helper. It is a tiny IPC server: it takes the control
socket on fd 3, registers handlers for the `ipc` function set (exec, wait, mount,
mkdir, create-file, symlink, pivot-root, umount, rmdir, unlink, open), and serves
requests until the socket closes. `glbx` spawns it inside the namespace stack and
drives the sandbox entirely through these calls; the handler internals and the
namespace model are covered in [the execution-environment
chapter](./exec-env.md). The `--stub` flags and `$GLBX_STUB_PATH` point `glbx` at
this binary.

### `cmd/taskdemo`

A standalone driver that exercises the progress UI end to end: it builds a
`taskui.TaskTracker`, runs a mix of succeeding, failing, and endless tasks through
the interactive overview (or the non-interactive viewer off a TTY), streaming
per-task logs, then serializes the tracker to a temp file and prints a
`--view-logs <path>` invitation to replay it. It is a manual harness for the
progress UI, not part of a build; the UI it drives is documented in [the logging
and progress-UI chapter](./logging-ui.md).
