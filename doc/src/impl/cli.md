# Command-line interface

## `cmd/glbx`

A hand-rolled subcommand dispatch over `os.Args[1]`, each command owning a
`flag.FlagSet`:

- `import` — import a source package into the working tree.
- `lockfile` / `lockfile-rootfs` — generate a package's build-tooling lock, or
  the image configuration-tooling lock.
- `build` — drive the engine over the discovered artifact graph, with the
  interactive progress UI; `--invalidate` drops a target's cache entry,
  `--view-logs` replays a saved run, and `--target <Key>` builds only that one
  node — with `--no-recurse` every other node must resolve from the cache (a
  miss is a hard error), so the pair builds exactly one node from cached inputs.
- `graph` — render the dependency graph as Mermaid, or a Gantt chart from a
  saved build-logs file.
- `cache` — object-store administration: `status`, keep-set `gc`, and raw
  `blobs` / `map` access.
- `restore-cache` — populate the object store from the pins in the working tree,
  downloading any missing source archive or tooling `.deb` by its recorded URLs
  and verifying each against its pinned hash.
- `publish` — mirror a checkout's locally-held content to an OCI registry:
  upload every recorded input and built-output blob the registry lacks and set a
  `map` alias per built identity. Reads the local store, writes the registry, and
  is idempotent. The target registry is `--registry` or `$GLBX_REGISTRY`.
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

## `cmd/exec_env_stub`

The sandbox helper — see [Execution environment](./exec-env.md).

## `cmd/taskdemo`

A standalone binary that drives the full progress UI with synthetic tasks,
exercising the UI end to end on its own.
