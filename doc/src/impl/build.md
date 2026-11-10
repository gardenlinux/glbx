# Package build and image assembly

## `internal/install`

A thin resolver wrapper plus the dpkg-based install of a resolved set into a
root filesystem inside the sandbox: bind the `.deb` blobs in, unpack-all then
configure-pending to break the pre-dependency cycles. `Bootstrap` creates a
minimal working root filesystem from an index's essential set, and
`BootstrapResolved` does the same from an explicit package set.
`ResetDpkgDatabase` empties the dpkg database in place, leaving every installed
file on disk, so a following install reasons only over the packages it is
given.

## `internal/build`

The integration layer where the engine, the sandbox, the Debian layer, and the
store meet.

- **Source build** (`DebianPkgBuild`): identity folds the source dirhash, the
  architecture, and its built-from dependency identities. Its build action
  assembles the chroot (pinned tooling unpacked in, locally built dependencies
  layered over, source tree and pinned archives staged onto a disk-backed scratch
  directory under the cache root — `<cache>/tmp`, bind-mounted in and removed
  after — rather than a RAM tmpfs), applies the content-derived local version,
  runs the Debian source build unprivileged, and harvests every produced `.deb`
  and its control stanza into the store.
- **Binary validation** (`debianBinaryPkg`): a built-from edge to its source
  build and carried-along edges to co-emitted siblings. Its build action checks
  that every runtime dependency is satisfiable from locally built binaries (with
  a per-binary `lockfile_deps` allowance for tolerated externals), runs an
  install check — bootstrap a working base from the pinned tooling, wipe the
  dpkg database so dpkg forgets the base while its files remain, then install
  the binary and its local closure so dpkg configures only our packages — and
  re-emits the validated `.deb`.
- **Rootfs** (`Rootfs`): one artifact per `rootfs-<label>.yml` variant in the
  working tree, keyed `rootfs:<label>:<arch>`. Its identity folds the label, the
  architecture, every transitive binary dependency, the pinned image-tooling
  hashes, and the content of the variant's `rootfs-<label>.conf.d` setup-script
  directory. Its build action narrows the runtime closure to that variant's
  shipping set, stages the three-layer overlay (payload / throwaway configuration
  tooling / mutations), runs dpkg configuration against the merged view, then runs
  the variant's conf.d setup scripts (see below), and — before unmounting, so the
  removals are recorded in the packed layer — drops the staging areas. It composes
  the final view from payload and mutations only and serializes the tree as a
  deterministic `tar.gz`. The image-configuration tooling lock (`rootfs-deps.yml`)
  is shared by every variant: it is the throwaway Layer 1, never shipped, so it
  does not depend on which packages a variant ships.

  A variant's post-configuration customization lives in an optional
  `rootfs-<label>.conf.d` directory of executable scripts. glbx copies each
  regular file in the directory into the rootfs and runs it inside the container,
  in byte-wise name order, with the rootfs as `/`; a script is run by its shebang
  (`#!/bin/bash`, `set -e` by convention) and a non-zero exit fails the build. The
  scripts are staged to a temporary in-rootfs directory removed before the tree is
  packed, so they leave no trace of their own. A missing or empty directory runs
  nothing — the base rootfs carries no built-in content policy. A script reached
  through a symlink is read with the link followed, so one variant's conf.d may
  symlink to another's to share a single source. The `/bin/sh` → `/bin/bash`
  symlink and the reproducibility-driven `/var/log` cleanse are supplied this way,
  not hardcoded.
- **Tooling build** (`ToolingPkgBuild`): an image-assembly tool's source build,
  keyed `tooling-pkg-build:<source>:<arch>` — a distinct key namespace and a
  distinct identity seed from `DebianPkgBuild`, so a same-named shipped source
  build and tooling build never collide on key or identity. It reuses the source
  build's chroot-assembly, changelog-stamp, and output-collection phases
  verbatim, but has no binary-validation layer: its build dependencies resolve
  only among other tooling builds (`ToolingSet`), its produced `.deb`s are its
  outputs, and those are consumed directly at image assembly. Its identity folds
  the tooling seed, the source dirhash, the architecture, and each tooling build
  dependency's identity.
- **Image** (`Image`): the disk-image artifact and the top of the graph, keyed
  `image:<name>:<arch>`, one per `images.yml` entry. Its identity folds the image
  seed, the name and architecture, the rootfs node's identity, the assembly
  script bytes, the `conf.yml` bytes, every pinned assembly-tooling file hash,
  each local-tooling build's identity, and each input (a `value`'s bytes, an
  `image` input's referenced identity) — so a change to the rootfs, the script,
  the tool set, a local tool, or an input rebuilds it. Its Depends are the one
  `Rootfs` node, each local-tooling `ToolingPkgBuild`, and each `Image` named by
  an `image` input (the only cross-universe edges). Its build
  action raw-extracts the pinned assembly tooling into a container root (local
  tooling overriding same-named mirror packages), then `dpkg --unpack` +
  `--configure --pending` registers and configures them so maintainer scripts run
  and alternatives resolve — the container is a real working system, not a bag of
  files. It stages the rootfs
  tarball read-only and the assembly script, stages each input under `inputs/`
  (a `value` as a regular file, an `image` input as a read-only bind mount of the
  referenced image's output blob, resolved by source since every image shares the
  one output name), runs the script by its shebang with
  `CAP_SYS_ADMIN` and no loop device (`$1` the rootfs, `$2` the output path,
  `GLBX_IMAGE_ID` in the environment carrying the image identity for deriving
  deterministic on-disk identifiers, and `GLBX_INPUTS_DIR` pointing at the staged
  inputs), and
  harvests the written image from the staging mount namespace into the store
  under the single output name `image`.

`PackageSet` scans the working tree and resolves shipped `<source>:<binary>`
references; `ToolingSet` does the same over `images.d/tooling/` for the assembly
universe, resolving tooling references only among tooling builds. `BuildGraph`
builds one `Rootfs` per variant and one `Image` per `images.yml` entry and lets
discovery walk their (shared) dependency closures into a single graph; with
`images.yml` present the roots are every image and every rootfs variant, so a
plain build builds them all, and absent it the roots are the rootfs variants
exactly as before.

- **Signing** (`signing.go` + `internal/signing`): when a source build's
  `build.yml` carries a `signing` block, the build starts a host-side signing
  service (`internal/signing`) per key on a `SOCK_SEQPACKET` socket, bind-mounts
  each socket into the container, sets `GLBX_SIGN_SOCKET`, and adds the PKCS#11
  forwarding-module package as an implicit dependency (installed and folded into
  identity, never written to a dependency file). Each key's certificate content
  is also folded into the build identity. The service owns the private key and
  the certificate and serves public key, certificate, and signatures over the
  socket behind a pluggable backend; the built-in backend invokes OpenSSL against
  a key file and signs ECDSA deterministically. See [Out-of-container
  signing](../concepts/out-of-container-signing.md).

## `internal/signing`

The host side of out-of-container signing: a `Serve` loop over a
message-boundary-preserving Unix socket answering public-key, certificate, and
sign requests (protobuf), and a `Backend` interface whose key-file implementation
shells out to OpenSSL. It holds the private key and certificate, and converts
OpenSSL's DER ECDSA output to the raw form PKCS#11 returns; the in-container
forwarding module (shipped as its own package) never sees the key, and the
certificate reaches the build only over the socket.
