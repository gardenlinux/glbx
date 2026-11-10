# glbx

**glbx** — the Garden Linux Build eXecutor — is a build system for complete Linux
operating-system images. It takes version-controlled sources and produces a
finished, bootable system — a kernel, a root filesystem, a disk image — in which
every package and every version was decided at build time. The output is not a
package feed to install from later; it is the image itself, immutable, with
nothing left to resolve or drift in the field.

What sets it apart is *how* it gets there. glbx builds everything that ships from
source, inside isolated sandboxes, and names every intermediate result by a hash
of its inputs. A few guarantees follow directly from that:

- **Reproducible.** Identical inputs produce identical outputs, anywhere. A build
  of unchanged sources is a cache lookup, not a recomputation, and a rebuild yields
  bit-for-bit the same bytes.
- **From source, provably.** Everything in a finished image is built from source.
  Not one binary is taken from a mirror or a third party and dropped into the
  output — the entire image traces back to sources under control.
- **Hermetic.** Each build sees only its declared inputs, in a sandbox an ordinary
  user can stand up — no Docker, no root on the host — so the machine a build
  happens to run on can never leak into the result.
- **Traceable.** The sources and the pins for every external input are reviewable
  in git; large binaries live in a content-addressed store, recoverable from a
  recorded hash and URL. Nothing a build consumes is unaccounted for.

## How it compares

glbx sits between two worlds and takes what is best from each.

From traditional Linux distributions — Debian, Fedora, Arch — it keeps the runtime
world people are familiar with: a standard filesystem-hierarchy layout, `dpkg`-managed
packages, glibc and the GNU userland, ordinary dynamically-linked binaries that
look and behave exactly as they do on any such system. A shipped image is a normal
Linux system, with none of the store-path indirection a Nix-style image carries in
its binaries and paths.

From reproducible build systems in the spirit of Nix, it takes the build
discipline: builds as pure functions of their declared inputs, content-addressed
identity, isolation as an invariant rather than a convention, and reproducibility
by construction. glbx borrows that rigor for *how it builds* — it does not adopt
the Nix store model or the NixOS image shape. The result compiles with Nix-like
guarantees but ships a thoroughly conventional distribution.

The combination is the point: the familiarity and leverage of an established
distribution, with the provenance and reproducibility guarantees usually reserved
for a purpose-built reproducible build system.

## Getting started

glbx is a Go program. `make build` compiles the `glbx` command and its sandbox
helper into `bin/`:

```console
$ make build
```

Building images needs a Linux host with a handful of ordinary facilities: a Go
toolchain, the Debian packaging tools glbx drives during a build (`dpkg`,
`dpkg-deb`, `dpkg-buildpackage`) plus `tar` and `gpg`/`gpgv`, and working
**unprivileged user namespaces** plus the `newuidmap`/`newgidmap` helpers and
`/etc/subuid` / `/etc/subgid` ranges for your user. No elevated privileges are
required beyond that.

glbx builds *from* a separate **configuration repository** — the one that holds
the packages, the rootfs variants, and the image definitions (the Garden Linux
`glbf-staging` repo is the reference example). From inside such a checkout:

```console
$ glbx build
```

With no target, this builds every image and every rootfs variant the repository
defines, printing each one's identity as it lands. The configuration directory is
discovered by walking up from the working directory, and built results are cached
under `~/.cache/glbx`.

To build just one node of the graph — a single package, a rootfs, or an image —
name it with `--target`:

```console
$ glbx build --target image:disk:amd64
```

`glbx graph` prints the dependency graph a build walks. Where it matters, later
chapters cover the flags these commands omit here — pointing the cache at a shared
registry (`$GLBX_REGISTRY`), overriding the cache location (`$GLBX_CACHE`), and the
rest.

## Where to next

- **[Vision](./concepts/vision.md)** — what glbx is for, and the ideals that shape
  it. The *why*.
- **[Architecture](./concepts/architecture.md)** — how the whole system fits
  together: the artifact graph, identity, the object store, the sandbox, source
  lineage, package building, images, and orchestration. One pass leaves you with
  the shape of the *how*.
- **The concept deep-dives** — one chapter per component
  ([identity](./concepts/identity.md), [the object store](./concepts/object-store.md),
  [the artifact model](./concepts/artifact-model.md),
  [source lineage](./concepts/source-lineage.md),
  [package build](./concepts/package-build.md),
  [image assembly](./concepts/image-assembly.md),
  [the execution environment](./concepts/exec-env.md), and the rest). Each stands
  on its own; read the one you need.
- **Implementation** — a per-package tour of the code under `internal/` and
  `cmd/`, for a reader working on glbx itself.
