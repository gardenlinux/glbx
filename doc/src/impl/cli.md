# Command-line interface

## `cmd/glbx`

A hand-rolled subcommand dispatch over `os.Args[1]`, each command owning a
`flag.FlagSet`:

- `import` — import a source package into the working tree.
- `lockfile` / `lockfile-rootfs` — generate a package's build-tooling lock, or
  the image configuration-tooling lock.
- `build` — drive the engine over the discovered artifact graph, with the
  interactive progress UI; `--invalidate` drops a target's cache entry and
  `--view-logs` replays a saved run.
- `graph` — render the dependency graph as Mermaid, or a Gantt chart from a
  saved build-logs file.
- `cache` — object-store administration: `status`, keep-set `gc`, and raw
  `blobs` / `map` access.
- `restore-cache` — populate the object store from the pins in the working tree,
  downloading any missing source archive or tooling `.deb` by its recorded URLs
  and verifying each against its pinned hash.
- `resolve` — resolve package names against an index, with machine-parseable
  output on stdout and logs on stderr.
- `status` — report the cache root and the discovered working tree.
- `exec-chroot` — unpack a stored root filesystem and run a command inside the
  sandbox stack.

## `cmd/exec_env_stub`

The sandbox helper — see [Execution environment](./exec-env.md).

## `cmd/taskdemo`

A standalone binary that drives the full progress UI with synthetic tasks — the
UI's end-to-end exercise, with no engine behind it.
