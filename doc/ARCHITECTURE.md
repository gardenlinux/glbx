# Architecture Sketch

This is a rough conceptual shape for glbx — enough to see how the goals in
[VISION.md](./VISION.md) hold together, not yet a detailed design. Each piece
named here is concretized on its own later; this sketch exists to show the
skeleton and how the parts connect.

## The central idea

> Everything glbx builds is an **artifact**. Each artifact has an **identity**
> derived from its inputs. The system looks that identity up in a
> **content-addressed store**; on a miss it builds the artifact inside a
> **hermetic sandbox** and stores the result under its identity; on a hit it
> reuses the stored result.

Every other part of the architecture is in service of that sentence: what an
artifact is, how its identity is computed, where results live, what "hermetic"
means, and how a graph of artifacts becomes an image.

## The pillars

### 1. The artifact graph

An artifact is the smallest unit that can be built, cached, and depended upon.
Artifacts form an explicit graph: dependencies are declared, never inferred from
the host or guessed from names. The build engine knows only how to walk this
graph — it has no built-in notion of "a package" or "an image". Concrete kinds
of artifact (building a source package, validating one of its binary outputs,
assembling an image) are specializations on top of the same uniform interface.
Building a target is the one general operation over the whole system: walk the
graph and build or reuse each artifact by identity. Most artifacts are simply
derived from their inputs this way; source packages are the exception, needing
preparation (bringing in their packaging and pinning their build tooling) before
they become buildable nodes.

### 2. Content-derived identity

An artifact's identity is a hash of its inputs: the hash of its source tree, the
target architecture, and the identities of each of its dependencies, folded
together deterministically. Identities are *derived*, not assigned. Two
artifacts with the same identity are the same artifact — so a cache hit is not
"probably equivalent", it is equivalent. A change to any leaf input changes that
leaf's identity, which rolls up through every artifact that depends on it. The
hash *is* the dependency ledger; no separate change-tracking database is needed.

### 3. The object store

There is exactly one place where results persist and are reused: an **object
store** made of two parts — a content-addressed store of immutable blobs,
addressed by the hash of their bytes, and a map from artifact identities to the
blobs they produced. Everything the system might want to reuse — source
archives, dependency lockfiles, built packages, assembled images — is a blob.
The store is a **cache**, not a system of record: everything in it is either a
build output that can be rebuilt or an external input that can be re-fetched from
a recorded location, so anything it drops can be reproduced. The durable source
of truth is git (see below); the store just saves the system from redoing work.

The store is local to each machine, but it is also meant to have a shared remote
form: artifacts built once in the CI pipeline can be pulled into a local store
and reused directly, rather than rebuilt on each developer's machine. The remote
cache is a way to distribute already-built outputs, not a dependency of being
able to build.

### 4. Hermetic sandboxing

Builds run inside isolation built directly on Linux kernel primitives
(user, mount, and PID namespaces) — not on an external container runtime. The
reason is to keep host requirements to a minimum: glbx does not prescribe
Docker, Podman, or any particular runtime being installed, it only relies on
facilities the kernel already provides. This lets a build run unprivileged, see
only its declared inputs, and leave the host untouched, while still letting
unchanged Debian tooling run "as root" inside the sandbox exactly as it expects.
Isolation is a property the architecture enforces, not a convention the build is
trusted to follow.

Eventually the build engine should also support running on other host operating
systems — for example via Virtualization.framework on macOS or WSL on Windows —
so that glbx is not confined to Linux hosts. That is a direction to grow into,
not something the initial design needs to deliver.

### 5. From-source via lockfiles

Building a package needs build-time tooling (compilers, helpers, headers) that
is *not* part of any shipped image. Each source package pins the exact set of
such tooling in a **lockfile** — an explicit, per-architecture list of the
precise external binaries required, each identified by content hash and
accompanied by where to retrieve it. Pinning happens as a step separate from
building; by the time a build runs, every external input it needs is already
fetched and verified. A build never re-resolves against a moving external
repository.

Ideally, most builds resolve all of their build-time tooling directly from the
content-addressed blobs already in the object store. The retrieval location
recorded alongside each pin is only consulted to populate the store when a blob
is not yet present; it does not belong on the hot path of a typical build. In
the common case, a build reads its tooling out of the store by hash and touches
no external repository at all.

A package's tooling pins are *not* part of its pristine upstream lineage (see
below): a lockfile is a resolution against a moving external archive at a moment
in time, not part of what upstream shipped. The pins live on the integration
side — alongside the package where it is assembled into a product — and each
package pins its own tooling, so updating a tool that one package needs does not
force every other package to rebuild. A consequence worth stating: because pins
travel with integration rather than with the source lineage, bringing a newer
package version to a long-lived branch does not silently pull newer build
tooling unless that is asked for. On the mainline, by contrast, the tooling will
be built so that updating a package to a newer version refreshes its tooling
pins in the same step automatically — not because source and tooling are
coupled, but simply because that update rebuilds the package anyway, so it may as
well build against current tooling.

### 6. Image assembly

An image is the top of an artifact graph. It takes the runtime closure of a
chosen set of packages, installs them into a fresh root filesystem, and packages
the result deterministically. This assembly step is also where the from-source
guarantee is enforced: if the runtime closure reaches a binary not built from
source, it fails.

## Reproducible inputs live in git

Everything needed to reconstruct a build is reviewable in a git repository: the
packaging and local modifications for each source package, the pin of each
orig tarball (content hash plus where to fetch it), and the pinned dependency
closure. Large binary inputs — orig tarballs and built `.deb` files — stay out
of git and live in the content-addressed store; git holds only a fallback URL
from which to reconstruct the object store entry in the cases where it is not
already in the cache. A hash alone is not enough: every external input records
both an authoritative content hash *and* a set of locations from which a fresh,
independent builder can obtain exactly those bytes. Mismatched bytes are
rejected, never silently accepted.

### Source lineage

Each source package carries an independent **import lineage**: a chain of
pristine commits, one per imported upstream version, each recording only that
version's packaging — no local modifications. Local and integration changes live
in the product history and are joined to the pristine imports via merge
commits, so that updating a package replays as an ordinary three-way merge of
(old upstream, local changes, new upstream). This keeps upstream state and local
work cleanly distinguishable, and keeps the full provenance of every shipped
file intact.
