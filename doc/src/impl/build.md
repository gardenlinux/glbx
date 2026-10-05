# Package build and image assembly

## `internal/install`

A thin resolver wrapper plus the dpkg-based install of a resolved set into a
root filesystem inside the sandbox: bind the `.deb` blobs in, unpack-all then
configure-pending to break the pre-dependency cycles. `Bootstrap` creates a
minimal working root filesystem from an index's essential set;
`BootstrapResolved` does the same from an explicit package set.

## `internal/build`

The integration layer where the engine, the sandbox, the Debian layer, and the
store meet.

- **Source build** (`DebianPkgBuild`): identity folds the source dirhash, the
  architecture, and its built-from dependency identities. Its build action
  assembles the chroot (pinned tooling unpacked in, locally built dependencies
  layered over, source tree and pinned archives staged), applies the
  content-derived local version, runs the Debian source build unprivileged, and
  harvests every produced `.deb` and its control stanza into the store.
- **Binary validation** (`debianBinaryPkg`): a built-from edge to its source
  build and carried-along edges to co-emitted siblings. Its build action checks
  that every runtime dependency is satisfiable from locally built binaries (with
  a per-binary `lockfile_deps` allowance for tolerated externals), runs an
  install check in a bootstrapped sandbox, and re-emits the validated `.deb`.
- **Image** (`Rootfs`): identity folds every transitive binary dependency and
  the pinned image-tooling hashes. Its build action narrows the runtime closure
  to the shipping set, stages the three-layer overlay (payload / throwaway
  configuration tooling / mutations), runs dpkg configuration against the merged
  view, composes the final view from payload and mutations only, and serializes
  the tree as a deterministic `tar.gz`.

`PackageSet` scans the working tree and resolves `<source>:<binary>` references;
`BuildGraph` turns an image's package list into a discovered graph.
