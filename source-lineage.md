# Source Lineage

This document describes the layout and git conventions of the **source
repository** — the repository glbx builds *from*. It concretizes the source
lineage introduced in the architecture sketch
([ARCHITECTURE.md](./ARCHITECTURE.md) §"Source lineage").

The source repository is distinct from glbx itself. glbx is the build executor;
it carries no packages. The packages, their upstream history, and their
integration into images all live in this separate repository, which glbx reads
when it imports, locks, and builds. glbx is a program; the source lineage is a
shape of history it understands.

## Two kinds of history in one repository

The source repository holds two kinds of history, kept strictly apart:

- **Pristine upstream history** — what Debian shipped, recorded verbatim, one
  commit per imported version. Local edits never touch these commits.
- **Integration history** — which package versions a branch has adopted,
  together with any local packaging changes and the pinned build inputs. This is
  where work happens.

The separation is what makes updates tractable. Bringing a package to a newer
upstream version is an ordinary three-way merge: the common ancestor is the old
pristine import, one side is the local integration state, the other side is the
new pristine import. Git resolves what did not conflict and surfaces exactly
what did.

## Independent per-package lineages

Each source package has its own **import lineage**: a chain of pristine commits,
one per imported upstream version, where each import has the previous import of
*the same package* as its parent. The first import of a package is an orphan
root — it begins a fresh history of its own. A package's upstream history stands
alone and joins an integration branch only through merges.

A lineage carries no reserved branch name; identity lives in the commits. Each
pristine commit records in its message a machine-readable **import marker**
carrying two facts:

- the *package* it belongs to,
- the *exact upstream version* it records.

Keeping identity in the commits rather than in a long-lived tracking branch per
package buys two things. First, it removes a class of mistakes: a tracking
branch is mutable shared state that can drift or be corrupted by a careless
operation — a merge in the wrong direction, a force-push, a stale local copy —
and the lineage would then be wrong even though every commit was fine. A marker
baked into the commit cannot be knocked out of place this way; the history
itself is the record, and it is read the same whatever branches happen to point
where. Second, it keeps adding a package to a single step: a new package arrives
as one pull request that introduces its first import and merges it, with no
prior setup of a dedicated branch to be created and maintained before the work
can land.

To find a package's current upstream baseline on a given branch, glbx walks the
full commit graph reachable from that branch — following every parent of a merge
commit, since the pristine imports are reached *through* the integration
merges — collects the commits whose marker names the package, and selects the
one that is not an ancestor of any other match. That unique newest-by-ancestry
import is the baseline. Ancestry decides which import is newest; timestamps and
version-string ordering never enter into it. If the matching commits do not line
up on a single chain, the lineage has forked, which glbx reports as an error.

Tags on accepted imports are a readable index over this history: a tag points at
a commit the graph already fully describes, so the authority is the commit graph
and its markers, with tags as convenience on top.

## What a pristine commit contains

A pristine import commit records **only upstream source**: the content needed to
reconstruct exactly what Debian shipped for that version. It holds no local
patches, no integration configuration, and no build-time tooling pins — tooling
pins are a resolution against a moving archive at a moment in time, so they live
on the integration side ([ARCHITECTURE.md](./ARCHITECTURE.md) §5), not on the
upstream chain.

What the committed content looks like depends on the Debian source format. Two
formats are valid in the history: `3.0 (quilt)` and `3.0 (native)`.

### `3.0 (quilt)` — the common case

A quilt-format package keeps upstream code and Debian packaging separate. The
packaging is small, hand-maintained, and benefits from line-by-line version
control, so it is committed into the tree; the upstream code is large and
binary, so it is held in the object store:

- The **`debian/` directory** (control, rules, changelog, the patch series under
  `debian/patches/`, …) is committed directly into the tree.
- The **upstream tarball(s)** live as blobs in the content-addressed object
  store, referenced from the commit by a **`sources.yml`** that pins each one by
  SHA-256.

A quilt package can have **more than one upstream tarball**. Alongside the main
`<pkg>_<version>.orig.tar.*`, the format allows additional *component* tarballs
named `<pkg>_<version>.orig-<component>.tar.*`, each extracted into a
subdirectory named for its component. They let one source package bundle several
independently released upstream pieces — documentation on its own release
cadence, a vendored library, a separately maintained submodule — while each
piece keeps its own identity. `sources.yml` therefore pins a *list* of archives;
a package with a single orig is just the one-entry case.

`sources.yml` is explicit YAML: a list of archives, each pinned by content hash
with an ordered set of locations to fetch it from on a cache miss:

```yaml
sources:
  - file: <pkg>_<version>.orig.tar.xz
    sha256: <exact-tarball-sha256>
    urls:
      - <debian-archive-url>
      - <debian-snapshot-url>
  - file: <pkg>_<version>.orig-docs.tar.xz
    sha256: <different-exact-tarball-sha256>
    urls:
      - <debian-archive-url>
      - <debian-snapshot-url>
```

Each entry carries, for one archive:

- its **`file`** name, which encodes the component (if any) through the
  `.orig-<component>.tar.*` convention — the main orig has no component suffix,
- its authoritative **`sha256`**,
- an ordered list of **`urls`**, the retrieval locations in order of preference.

The hash is the identity; the URLs only say where a builder *obtains* those
bytes. Bytes that do not match the hash are rejected — a retrieval location
cannot override the pin. In normal operation the URLs are almost never touched:
an archive is fetched once, cached in the object store, and read from there by
hash on every build afterward, reaching no external service. They serve the cold
case where the store is empty and an archive must be reconstructed from scratch.
The conventional order follows from that: the current Debian archive first, fast
while the version is current, then a Debian snapshot URL as the durable fallback
that keeps old versions long after they leave the live archive. Because the
snapshot is only ever hit on this cold path, its slow performance is an
acceptable price for always having a way back.

The schema mirrors the `files` arrays in the build-input pins on the integration
side ([ARCHITECTURE.md](./ARCHITECTURE.md) §5): the same `sha256` + ordered
`urls` shape for a pinned, content-addressed input, so both read alike. The
difference is only what varies per entry — upstream archives split by *component*
rather than by architecture, as orig tarballs are architecture-independent.

### `3.0 (native)` — the self-contained case

A native package draws no line between upstream and Debian packaging: a single
tarball holds the whole source tree, `debian/` included, with no separate orig.
For these, the **entire source tree is committed directly into git** — there is
no `sources.yml`, since there is no large external archive to keep out of the
tree. Native packages are uncommon but fully supported.

### Legacy `1.0` is normalized on import

Debian's old `1.0` source format — an orig tarball plus a flat `.diff.gz`, or a
single native tarball — is **converted to `3.0 (quilt)` at import time**: the
diff becomes a proper `debian/` tree with a quilt patch series, and the
committed result is a modern quilt package. The history therefore holds exactly
two source formats, `3.0 (quilt)` and `3.0 (native)`; everything downstream —
build, patch tooling, review — sees only those two shapes.

## How a lineage joins an integration branch

A pristine lineage builds nothing on its own; it is a faithful record of
upstream. A package enters a build when the integration history merges an
integration branch — `main`, a release branch, or the like — with the pristine
import commit. The merge brings the import's tree into the branch and keeps the
full ancestry of both sides, so the upstream chain stays visible through it.
Local packaging changes and the pinned build inputs are then ordinary commits on
the integration branch, on top of the merged-in upstream state.

The mechanics of integration — where a package's tree sits on an integration
branch, how an update is proposed and merged, how pins are attached, and how
this is automated — are covered in their own documents. This one fixes the layer
beneath them: what upstream history looks like, and what each pristine commit
may contain.
