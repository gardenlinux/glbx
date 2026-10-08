# Command-line interface

## `cmd/glbx`

A hand-rolled subcommand dispatch over `os.Args[1]`, each command owning a
`flag.FlagSet`:

- `import <package>` — import a Debian source package into the working tree. By
  default the import is recorded on the package's independent upstream lineage: a
  first import is an orphan commit, each later import is parented on the previous
  import, and the commit — carrying a fenced metadata block (`pkg`, `version`,
  `auto_update`) in its message — is built entirely through git plumbing against
  a throwaway index, so the working tree is never disturbed by the construction.
  The command then replaces itself with `git merge` to bring the import into the
  current branch, so a merge conflict (or clean merge) propagates verbatim as the
  command's own output and exit status. When a prior import already pins the exact
  version resolved from the archive, the import short-circuits before any download
  (`already present, nothing to do`). `--no-git-history` writes `pkgs/<package>/`
  directly with no commit; a non-git working tree falls back to that mode with a
  warning.
- `lockfile` / `lockfile-rootfs` — generate a package's build-tooling lock, or
  the image configuration-tooling lock.
- `build` — drive the engine over the discovered artifact graph, with the
  interactive progress UI; `--invalidate` drops a target's cache entry,
  `--view-logs` replays a saved run, and `--target <Key>` builds only that one
  node — with `--no-recurse` the target's dependencies must resolve from the
  cache (a missing dependency fails the target), so the pair builds exactly one
  node from cached inputs and its exit status reflects the target alone.
  `--stream` (with `--target` + `--no-recurse`) forwards the target's logs live
  to the console instead of the UI, for a legible build log on a CI runner.
- `graph` — render the dependency graph as Mermaid (default), a deterministic
  machine-readable node/edge export with `--format=json`, or a Gantt chart from
  a saved build-logs file. `--check-built` adds to the JSON a per-node map of
  which nodes are already present in `$GLBX_REGISTRY`, so a driver can skip them.
- `cache` — object-store administration: `status`, keep-set `gc`, and raw
  `blobs` / `map` access.
- `restore-cache` — populate the object store from the pins in the working tree,
  downloading any missing source archive or tooling `.deb` by its recorded URLs
  and verifying each against its pinned hash.
- `publish` — mirror a checkout's locally-held content to an OCI registry:
  upload every recorded input and built-output blob the registry lacks and set a
  `map` alias per built identity. `--target <Key>` scopes it to one built node's
  manifest, output blobs, and alias. Reads the local store, writes the registry,
  and is idempotent. The target registry is `--registry` or `$GLBX_REGISTRY`.
- `resolve` — resolve package names against an index, with machine-parseable
  output on stdout and logs on stderr.
- `status` — report the cache root and the discovered working tree.
- `exec-chroot` — unpack a stored root filesystem and run a command inside the
  sandbox stack.

The commands that open the object store for building or fetching — `import`,
`lockfile` / `lockfile-rootfs`, `build`, `cache`, `restore-cache`, and `status` —
route through `$GLBX_REGISTRY` (host[:port]/repo) when it is set, wiring a
pull-through store so a local miss is served from the registry;
`$GLBX_REGISTRY_INSECURE` selects plaintext HTTP for a local registry. `publish`
reads the local store directly — never through the registry it fills — so it
ignores `$GLBX_REGISTRY` for reads and takes its target from `--registry` or
`$GLBX_REGISTRY`.

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
