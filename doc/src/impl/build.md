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
- **Image** (`Rootfs`): identity folds every transitive binary dependency and
  the pinned image-tooling hashes. Its build action narrows the runtime closure
  to the shipping set, stages the three-layer overlay (payload / throwaway
  configuration tooling / mutations), runs dpkg configuration against the merged
  view, then — before unmounting, so the removals are recorded in the packed
  layer — drops the staging area and empties `/var/log`, whose dpkg traces vary
  run to run. It composes the final view from payload and mutations only and
  serializes the tree as a deterministic `tar.gz`.

`PackageSet` scans the working tree and resolves `<source>:<binary>` references;
`BuildGraph` turns an image's package list into a discovered graph.

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
