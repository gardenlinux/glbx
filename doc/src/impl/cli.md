# Command-line interface

## `cmd/glbx`

A hand-rolled subcommand dispatch over `os.Args[1]`, each command owning a
`flag.FlagSet`:

- `import <namespace>:<package>` — import an upstream source package into the
  working tree. The namespace selects the import provider and is always explicit
  (`debian:<package>` today): import creates `pkgs/<package>/`, so there is no
  `build.yml` to dispatch on. By default the import is recorded on the package's
  independent upstream lineage: a first import is an orphan commit, each later
  import is parented on the previous import, and the commit — carrying a fenced
  metadata block (`pkg`, `version`, `auto_update`) in its message — is built
  entirely through git plumbing against a throwaway index, so the working tree is
  never disturbed by the construction. The command then replaces itself with `git
  merge` to bring the import into the current branch, so a merge conflict (or
  clean merge) propagates verbatim as the command's own output and exit status.
  When a prior import already pins the exact version resolved from the archive,
  the import short-circuits before any download (`already present, nothing to
  do`). `--update-tag <tag>` sets the `auto_update` value recorded in the commit
  (default `debian:<dist>`); setting it decouples the recorded series from the
  dist actually pulled from, so an import from a frozen snapshot can record the
  logical series it stands in for. `--no-merge` stops after building the import
  commit and prints a single JSON object on stdout (`{pkg, version, commit,
  first_import, already_present}`) with all logging on stderr, leaving HEAD and
  the working tree untouched for a caller to merge the commit itself; an
  already-present version prints an empty `commit` with `already_present` true.
  `--no-git-history` writes `pkgs/<package>/` directly with no commit; a non-git
  working tree falls back to that mode with a warning. Provider-specific options
  (repository, distribution, snapshot endpoint, keyring, signature verification)
  travel on `--params key=val,...`; `--params-help <namespace>` prints what a
  namespace accepts.
- `get-pkg-metadata` — list the commits reachable from HEAD in topological order
  and print the most recent import metadata for each package that currently has a
  directory under `pkgs/`. Topological order places every commit before its
  ancestors, so the first import marker seen for a package is its newest import and
  anything behind it is an older version of the same lineage. A package whose
  `pkgs/` directory is absent from the working tree is filtered out even when its
  lineage is still reachable, so removing the directory drops it immediately,
  committed or not. Each entry carries the import `commit` hash plus the recorded
  `pkg`, `version`, and `auto_update`. `--format` selects `yaml` (default) or
  `json` output. `--conf-dir` selects the configuration directory (the working
  tree holding `pkgs/`), auto-discovered from the working directory when omitted.
- `check-updates [package ...]` — report, for each present package, whether the
  version an import would select from the configured repo/dist is newer than the
  version currently pinned on its lineage. The candidate set is the present
  packages (`get-pkg-metadata`'s list); `--update-tag <tag>` keeps only those
  whose recorded `auto_update` matches, and positional package names narrow it
  further. The Sources index is loaded once and queried for every candidate, so
  checking many packages costs a single archive-metadata fetch; `--cookie` keys
  that InRelease in the store so a later run (or a following `import`) reuses the
  same signed snapshot. `--format text` (default) prints one `name old -> new`
  line per updatable package on stdout with all logging on stderr; `--format
  json` prints an array of `{pkg, old, new}`. A pinned package the archive no
  longer offers is logged and skipped, not reported as an update. `--repo`,
  `--dist`, `--keyring`, and `--no-verify` select the archive to compare against,
  and `--conf-dir` the working tree.
- `lockfile <target>` — generate a pinned dependency lock. The `<target>` is
  `<namespace>:<arg>`, or a bare namespace for a zero-argument one, selecting the
  lock generator:
  - `pkg:<name>` — the recommended developer form. It reads `pkg_type` from the
    package's `build.yml` and dispatches to the matching concrete generator, so
    the developer need not know whether a package is plain Debian or a special
    case. Writes `pkgs/<name>/build-deps.yml`.
  - `rootfs` — the image configuration-tooling lock. Writes top-level
    `rootfs-deps.yml`.
  - `debian:<name>` / `debian-kernel:<name>` — the concrete generators, invoked
    directly for debugging.

  Provider-specific options travel on `--params key=val,...`; `--params-help
  <namespace>` prints what a namespace accepts (for `pkg`, the concatenation of
  its delegates' help under each `pkg_type` heading). `--stub` locates the
  `exec_env_stub` binary a container-backed generator needs; generators that run
  no container ignore it.
- `build` — drive the engine over the discovered artifact graph, with the
  interactive progress UI; `--invalidate` drops a target's cache entry, and
  `--target <Key>` builds only that one node — with `--no-recurse` the target's
  dependencies must resolve from the cache (a missing dependency fails the
  target), so the pair builds exactly one node from cached inputs and its exit
  status reflects the target alone. `--stream` (with `--target` + `--no-recurse`)
  forwards the target's logs live to the console instead of the UI, for a legible
  build log on a CI runner. `--list-outputs <Key>` prints the named outputs of an
  already-built target (one name per line), and `--output-path <Key> <output>`
  prints the cache path of one named output's blob — both resolve the Key to an
  identity, read its manifest, and report the miss if the target is not built.
  Every build stores each node's log as a cache blob keyed by the node's
  identity, the engine's scheduling narrative as a per-run blob, and a run record
  keyed by a random run id tying them together; it prints that id so the logs can
  be reviewed afterward.
- `build --view-logs` — review stored build logs, never a live build. With no
  arguments it works from the current configuration tree: it shows the
  interactive node list — a node with an output manifest is green, a node with a
  stored log but no manifest (a failed attempt) is red, an unbuilt node is empty —
  and prints a node's log on selection. A positional `<target>` prints just that
  node's log and exits instead of opening the UI. `--run <id>` draws the node
  list, states, and logs from a recorded run rather than the current tree, so a
  past run stays viewable even after a rebuild; `--run <id> <target>` prints one
  node's recorded log, and `--run <id> --engine-logs` prints that run's engine
  scheduling narrative (the only way to surface it — it never appears in the node
  UI). `--report <file>` draws the node list, states, and logs from a
  reproducibility report instead of a run: a reproduced node is green, a mismatch
  or rebuild failure is red, and each node's log loads by the blob hash the report
  records; `--report <file> <target>` prints one node's reproducibility log.
- `build --check-reproducible` — rebuild already-built targets and check that each
  reproduces its cached output. With `--target <Key>` it checks one node;
  otherwise it checks every node a whole-tree build would build. The run is flat
  and bounded by `--jobs`: every target rebuilds in parallel with no build-order
  edges, each taking its inputs from its dependencies' *original* cached outputs
  rather than a freshly rebuilt one, so a non-reproducible dependency never
  cascades into spurious mismatches downstream. A target that is not already built
  is a hard error. Reproducibility is judged by the output-manifest hash: a
  rebuild whose manifest matches the cached one is green, a mismatch or a rebuild
  failure is red, and the command exits non-zero if any target does not reproduce.
  The run writes only blobs and no map entries — differing output blobs, the
  reproduced manifest blob, and each rebuild's log — so it never perturbs what the
  normal build and `--view-logs` paths resolve. A JSON report (`--report <file>`,
  required) pairs each artifact's original and reproduced manifest hash and
  records the hash of its log blob, which is the only reference to that log.
- `graph` — render the dependency graph as Mermaid (default), a deterministic
  machine-readable node/edge export with `--format=json`, or a Gantt chart of a
  recorded build run with `--gantt <id>`. Each JSON node carries its `key` (the
  unique build handle, which edges reference) and a display-only `shortName` (the
  package name or `rootfs`, no type prefix). `--check-built` adds to the JSON a
  per-node map, keyed by `key`, of which nodes are already present in
  `$GLBX_REGISTRY`, so a driver can skip them.
- `cache` — object-store administration: `status`, keep-set `gc`, and raw
  `blobs` / `map` access.
- `restore-cache` — populate the object store from the pins in the working tree,
  downloading any missing source archive or tooling `.deb` by its recorded URLs
  and verifying each against its pinned hash.
- `publish` — mirror a checkout's locally-held content to an OCI registry:
  upload every recorded input and built-output blob the registry lacks and set a
  `map` alias per built identity. A built node's stored log is mirrored alongside
  its outputs — only ever for a node whose manifest is also being published, so a
  log is never pushed without the outputs it belongs to. `--target <Key>` scopes
  it to one built node's manifest, output blobs, log, and aliases. Reads the
  local store, writes the registry, and is idempotent. The target registry is
  `--registry` or `$GLBX_REGISTRY`.
- `resolve` — resolve package names against an index, with machine-parseable
  output on stdout and logs on stderr.
- `status` — report the cache root and the discovered working tree.
- `exec-chroot` — unpack a stored root filesystem and run a command inside the
  sandbox stack.

The commands that open the object store for building or fetching — `import`,
`lockfile`, `build`, `cache`, `restore-cache`, and `status` — route through
`$GLBX_REGISTRY` (host[:port]/repo) when it is set, wiring a pull-through store
so a local miss is served from the registry; `$GLBX_REGISTRY_INSECURE` selects
plaintext HTTP for a local registry. `publish` reads the local store directly —
never through the registry it fills — so it ignores `$GLBX_REGISTRY` for reads
and takes its target from `--registry` or `$GLBX_REGISTRY`.

A registry that requires authentication is reached with `$GLBX_REGISTRY_TOKEN`
(and `$GLBX_REGISTRY_USER`): on a `401` bearer challenge the client redeems a
token from the challenge's realm using those credentials as HTTP basic auth,
caches it, and retries. With no token set, requests go out unauthenticated, as a
local or anonymous registry expects.

## `cmd/exec_env_stub`

The sandbox helper — see [Execution environment](./exec-env.md).

## `cmd/taskdemo`

A standalone binary that drives the full progress UI with synthetic tasks,
exercising the UI end to end on its own.
