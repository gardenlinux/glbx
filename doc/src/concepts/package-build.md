# Package Build

This document describes how one source package is turned into the binary
`.deb`s that downstream artifacts consume. It builds on
[source-lineage.md](./source-lineage.md): the source — a `debian/` tree plus the
orig archive(s) pinned in `sources.yml`, or a native tree committed whole — is
assumed already present on the branch being built. What follows is everything
*between* having that source and having validated, downstream-usable binaries:
how the build-time tooling is pinned, how the build itself runs, and how its
outputs are shaped into graph artifacts.

## Two populations of binaries

## Where a build's inputs come from

The `.deb`s that go into a build come from two sources, and the whole design
turns on keeping them apart:

- **External tooling** — `.deb`s fetched from Debian: compilers, `debhelper`,
  and the like, together with their transitive dependencies. These are used only
  while compiling and end up in no shipped image, so it is acceptable to take
  them from a third party rather than build them ourselves.
- **Locally built binaries** — the outputs of our own source builds. One build's
  outputs routinely become another build's inputs, and everything that ships
  must, all the way down, trace back to these.

This split is about *provenance* — where a `.deb` comes from — not about *when*
it is used. External tooling is always a build-time input. But a locally built
binary can serve either role: it may be a build-time input to another source
build (for the cases where building against our own copy is required), and it is
the input a downstream artifact depends on at runtime. The per-binary validation
stage described below in particular draws nearly all of its inputs from our own
builds.

What has to be pinned is the external slice. A locally built input is already
fully determined — its content hash rolls up from its own source and inputs, so
the artifact graph pins it by construction. An external `.deb` has no such
anchor on its own: Debian's archive moves, so the same request on a different
day can yield different bytes. Taking tooling from a third party is fine, but
only if it is done *reproducibly* — every external input pinned to exact bytes.
That pinning is `build-deps.yml`.

The source lineage already fixes the first stage of reproducibility: the exact
upstream source. This document fixes the second: the exact external tooling, and
the way the build consumes it alongside locally built inputs.

## Pinning external tooling: `build-deps.yml`

A source package's external build-time inputs are pinned in a
**`build-deps.yml`** that lives next to the package on the integration branch —
not on the pristine lineage, since the pin is a resolution against a moving
archive at a moment in time, not part of upstream source (see
[source-lineage.md](./source-lineage.md) and
[ARCHITECTURE.md](./architecture.md) §5). The pin is **per package**: each
package locks its own tooling, so bumping a tool one package needs does not
force every other package to rebuild.

`build-deps.yml` records only the external `.deb`s — the ones that would
otherwise be resolved against a moving Debian archive. Locally built inputs are
not listed here; they come from the artifact graph, already pinned by their own
content-derived identity. The file is explicit YAML, listing each external
package by name and exact Debian version, and for each one an array of concrete
file objects — one per architecture variant — each carrying the file's content
hash and an ordered list of URLs to fetch it from on a cache miss:

```yaml
build_deps:
  - name: gcc-14
    version: "14.2.0-3"
    files:
      - arch: amd64
        sha256: <exact-deb-sha256>
        urls:
          - <debian-archive-url>
          - <debian-snapshot-url>
      - arch: arm64
        sha256: <different-exact-deb-sha256>
        urls:
          - <debian-archive-url>
          - <debian-snapshot-url>
  - name: debhelper
    version: "13.20"
    files:
      - arch: all
        sha256: <exact-deb-sha256>
        urls:
          - <debian-archive-url>
          - <debian-snapshot-url>
```

`arch` is either a concrete Debian architecture (`amd64`, `arm64`, …) or `all`
for an architecture-independent `.deb`. Each tool entry carries **either** a set
of per-architecture files (one per supported concrete architecture) **or** a
single `all` file — never a mix. A tool is thus available for every architecture
the system targets, or it is an arch-independent file that serves all of them.

As with `sources.yml`, the hash is the identity and the URLs are only how a
builder *obtains* the bytes when the object store does not already hold them;
bytes that do not match the hash are rejected, and in normal operation the URLs
are never touched because the object store already has every pinned file. The
URL ordering follows the same convention: the live Debian archive first, a
snapshot URL as the durable cold-start fallback.

### The lockfile records availability, not use

`build-deps.yml` is deliberately **not** authoritative about which tools a given
build actually installs. It records the full set of tooling *available* to the
package across all architectures; selecting which of those files to install for
a particular architecture happens later, when the build chroot is assembled.

This keeps lockfile generation simple and architecture-symmetric. If a package
needs a certain tool only when building on one architecture, the generator still
records that tool unconditionally — resolved for every architecture — rather
than encoding per-architecture conditionals about what is strictly required. The
cost is a few unused entries in the file; the benefit is that generating the
lockfile does not have to reason about architecture-specific build logic, and
the file reads the same for every architecture. The build step simply takes what
it needs from the available set and ignores the rest.

## Declaring dependencies: `build.yml`

`build-deps.yml` and `sources.yml` are machine-generated pins. The one
hand-authored file per package is **`build.yml`**, which declares how the
package relates to the *rest of the artifact graph* — the edges to other
packages' builds and binaries — along with the knobs that control the build
itself (active Debian build profiles, build options, extra environment).

The dependency declarations are the important part, because they are where the
artifact graph's edges come from. There are two distinct kinds, kept separate
because they answer different questions: what a package needs *to compile*, and
what its individual output binaries need *at runtime*. Conflating them — as a
single flat "dependencies" list would — causes either needless rebuilds or
unbuildable cycles.

### `build_depends` — compile-time edges

`build_depends` names the *outputs of other source builds that this package
needs present in its build chroot at compile time*. Each entry identifies a
binary produced by another package's build (`<source>:<binary>`); that binary's
`.deb` is layered into the chroot before `dpkg-buildpackage` runs, overriding
any same-named external tooling.

This is the heavyweight edge: it is a build-order dependency on another source
build, so whenever that build's identity changes, this package rebuilds. It is
declared **per source package**, because the compile sees one shared chroot
regardless of how many binaries the source ultimately emits. These are the edges
to keep few — see [Keep the graph shallow](#keep-the-graph-shallow).

### `runtime_depends` — per-binary runtime edges

`runtime_depends` names, *for one output binary*, the other locally built
binaries that belong in its runtime closure. It is a map keyed by output binary
name, because different binaries from the same source have different runtime
needs: a `-dev` binary depends on its matching runtime library, two co-emitted
libraries may depend on each other, and so on.

These edges do not force a rebuild of this package and may form cycles among
co-emitted siblings (a build emits them together in one run, so neither waits
for the other). They exist to describe what a binary *drags into an image's
runtime closure*, and they are what the binary-package validation stage checks
for local satisfiability.

## Building a source package

The expensive operation is compiling one source package into its binaries. It is
exactly a Debian source build — `dpkg-buildpackage` — run inside a hermetic
sandbox so that it sees only its declared inputs and leaves the host untouched.

### Assembling the build chroot

The sandbox is built from Linux namespaces directly (user, mount, PID — see
[ARCHITECTURE.md](./architecture.md) §4), with no external container runtime.
Into a private root filesystem the build assembles:

1. **The build-time tooling.** For the target architecture, the relevant files
   from `build-deps.yml` are taken from the object store (by hash) and installed
   into the chroot. Installation uses dpkg in two phases — unpack everything,
   then configure — so that the `Pre-Depends` cycles in Debian's base tooling
   resolve.
2. **The locally built dependencies.** Each binary named in the package's
   `build_depends` — an output of another source build — is layered in on top
   of the tooling. Where a locally built package and a tooling package share a
   name, the locally built one wins: the build links against what we built, with
   the external tooling filling in only what we do not build ourselves.
3. **The source tree.** The `debian/` tree and the orig archive(s) — fetched
   from the object store by the hashes in `sources.yml` — are combined into the
   full source tree ready for `dpkg-buildpackage`. (A native package is already
   a whole tree and needs no orig.)

Blobs are bind-mounted from the object store rather than copied, so assembling a
chroot does not duplicate hundreds of megabytes of `.deb`s per build. The build
then runs `dpkg-buildpackage` as an unprivileged user inside the sandbox — the
user namespace lets it believe it is root with respect to the chroot while being
unprivileged on the host — and the resulting `.deb`s are read back out into the
object store.

### Deterministic local versioning

Each locally built `.deb` is given a synthetic version suffix derived from the
content hash of its source tree, sorting just above the corresponding plain
Debian version. The suffix changes if and only if the source changes, so it
carries no wall-clock time or git metadata, and it ensures a locally built
package overrides a same-named Debian one during installation. The suffix is
applied by prepending a synthetic `debian/changelog` entry as the source tree is
staged into the chroot, leaving the committed changelog untouched.

## Two artifact stages: source build and binary package

A single source build emits *many* `.deb`s in one run, but downstream consumers
depend on individual binaries, and only some of a source's binaries are ever
used. The build is therefore modelled as two distinct artifact kinds:

- A **source-build** artifact: the one expensive `dpkg-buildpackage` run. It
  emits every `.deb` the source produces into its output manifest. There is one
  of these per source package, built once and cached.
- A **binary-package** artifact: a cheap, per-binary node that does no
  compiling. It selects one named `.deb` from a source build's outputs and
  *validates* it — that it exists, that it installs cleanly, and that its
  runtime dependencies are satisfiable from other locally built binaries. It
  re-emits that `.deb` as the thing downstream artifacts actually depend on.

A binary-package artifact is instantiated only when something downstream depends
on that binary. Downstream dependencies always point at binary-package
artifacts, never directly at the source build.

### Why re-emit through a validating binary stage

The point of the split is that **validation applies only to the binaries we
actually use.** A Debian source routinely produces outputs we have no interest
in, whose dependency metadata reaches into corners of Debian we never build from
source. If a source build had to stand or fall as a single artifact covering
*every* binary it emits, one unused output's unsatisfiable runtime dependency
would block the whole package — and transitively drag in a stack of packages
nothing ships.

Splitting the per-binary validation out fixes this cleanly. The source build
runs and emits all its `.deb`s regardless. Only the binaries that something
downstream selects get a binary-package artifact, and only those get their
runtime closure checked. An emitted-but-unused binary simply sits in the
manifest, its dependency metadata never inspected, because no node in the graph
ever asked for it. The graph of binary-package artifacts *is* the specification
of which outputs have to be sound. This per-binary validation is also where the
from-source guarantee is enforced: a binary's runtime dependencies must resolve
to other locally built binaries, so by the time an image is assembled its
runtime closure is local by construction.

## Keep the graph shallow

How a package declares its dependencies shapes the whole artifact graph, and the
graph's *depth* governs how much has to rebuild when something changes and how
much can build in parallel. A dependency edge from one source build to another
means: whenever the dependency's identity changes, this package rebuilds too. A
deep chain of such edges turns a single low-level change into a long serialized
cascade of rebuilds.

The guiding preference is therefore to **keep `build_depends` as small as
possible**, pushing compatibility checks onto the lightweight binary-package
stage wherever a true compile-time edge is not needed.

The canonical case is the C library. A package does not generally need our
freshly built libc *present in its build chroot*; it needs to compile against
*a* compatible libc, and the build-time tooling pinned in `build-deps.yml`
already supplies one. So rather than putting our libc in the package's
`build_depends` — which would rebuild the entire distribution on every libc
change — the package builds against the tooling's libc, and the real, locally
built libc enters only through the output binaries' `runtime_depends`. The
binary stage's install and locality checks then confirm the produced binary is
compatible with the libc we ship, without having forced a rebuild when that libc
moved. The same reasoning applies to most shared libraries.

The exception is genuine tight coupling: where a package must be built against
the exact version of a dependency it will run against — ABI-sensitive pairings,
or anything where building against the tooling copy and running against ours is
not safe — a real `build_depends` edge is correct and should be declared. The
principle is not "never depend"; it is "take a compile-time edge only when the
coupling is real," so that the common case stays shallow and the graph exposes
the maximum degree of parallelism.

## See also

- Where the source comes from: [source-lineage.md](./source-lineage.md).
- The artifact, identity, object-store, and sandbox concepts this builds on:
  [ARCHITECTURE.md](./architecture.md).
