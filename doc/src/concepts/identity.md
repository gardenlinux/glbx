# Identity

This document fixes how an artifact's **identity** is computed — the one value
the architecture sketch ([ARCHITECTURE.md](./architecture.md) §2) and the
artifact model ([artifact-model.md](./artifact-model.md)) both lean on but
deliberately leave abstract. Those documents establish *what* identity must be:
a deterministic, collision-resistant value over an artifact's inputs and the
identities of the artifacts it is built from, computable by walking the Depends
graph from the leaves up before anything is built. This one defines the bytes:
the hash function, how a source tree is reduced to a single value, and how the
pieces are ordered and folded so the same inputs always yield the same identity
and any difference yields a different one.

Everything here is a pure function of values already in hand — source bytes,
leaf inputs, and dependency identities. Computing an identity reads no store,
runs no build, and touches no sandbox. It is the step that happens first.

## The hash function

All hashing uses **SHA-256**. One algorithm is used everywhere — for hashing a
blob's bytes, for reducing a directory tree, and for folding an artifact's parts
into its identity — so that every hash in the system is the same width and the
same kind of value, and a blob's content address and an artifact's identity are
drawn from one space.

An **identity** is the 256-bit digest, handled as its lower-case hex string. It
is an opaque token: nothing reads structure out of it, nothing sorts by it to
decide build order, and its only operations are equality and use as a store key.
Collision resistance is what lets equality of identity stand in for equality of
inputs — the assumption the whole cache rests on.

## Hashing a source tree

An artifact's largest leaf input is its source tree, and reducing a tree to a
single hash — a **dirhash** — is the one non-trivial piece. The requirement is
that the dirhash depend on exactly the content that affects a build and on
nothing else: the same files with the same bytes and the same executable bits
must give the same dirhash on any machine, in any checkout order, under any
filesystem.

A dirhash is computed **recursively**, one directory at a time, so that each
directory's hash is a fold over its *immediate* entries alone. The hash of a
directory is taken over its entries in a **fixed order** — sorted by name,
byte-wise — so that the order the filesystem happens to return entries in never
matters. For each entry the computation folds in:

- its **name** within this directory, as bytes;
- its **type** — regular file, directory, or symbolic link;
- for a regular file, whether its **executable bit** is set, and the hash of its
  **contents**;
- for a directory, its own **dirhash**, computed the same way;
- for a symbolic link, its **target** as bytes.

The dirhash of the whole tree is the hash of its root directory under this rule.
Because each entry carries only its name and a subdirectory contributes its own
dirhash rather than a flattened list of root-relative paths, the construction is
genuinely recursive: a subtree hashes to the same value wherever it sits, and a
directory's hash is fully determined by its contents and never by where the
directory lives in a larger tree. This is also what keeps the computation
local — hashing a directory needs only that directory's entries and its
children's dirhashes, nothing from above it.

Nothing else is folded in. Owner and group, the full permission bits beyond the
single executable distinction, access and modification times, inode numbers, and
any other filesystem metadata are all excluded, because none of them affect what
a build produces and all of them vary between checkouts. Reducing permissions to
one executable/not-executable bit matches what the build and the resulting
packages actually preserve; carrying the raw mode would make the dirhash depend
on a checkout's umask.

The result is that two checkouts of the same source commit, on two machines,
produce byte-identical dirhashes — which is what lets an identity computed on one
machine match a store populated by another.

### Source trees that live partly in the object store

The source lineage keeps large upstream bytes out of git: a `3.0 (quilt)`
package commits its `debian/` directory into the tree but holds its upstream
tarball(s) as blobs in the object store, referenced by a `sources.yml` that pins
each archive by SHA-256 ([source-lineage.md](./source-lineage.md)). The dirhash
must still cover the complete source — the committed packaging *and* the
upstream content — or two packages sharing a `debian/` but differing upstream
would collide.

This falls out of the pinning already in place rather than needing a second
mechanism. The committed tree, `sources.yml` included, is hashed as above; and
because `sources.yml` pins every upstream archive by its SHA-256, those pins are
part of the committed bytes and so are already inside the dirhash. The upstream
tarballs do not need to be fetched and folded in separately — their content is
bound into the dirhash transitively through the hashes recorded in
`sources.yml`. A change to upstream content means a different archive, a
different SHA-256 in `sources.yml`, a different committed tree, and so a
different dirhash. A `3.0 (native)` package, whose whole tree including `debian/`
is committed with no external archive, is simply the case where the dirhash
covers everything directly.

## Folding an artifact's identity

With the dirhash defined, an artifact's identity is a fold over an **ordered
tuple** of its parts. The tuple is assembled in a fixed structure, each part is
reduced to bytes unambiguously, and the whole is hashed with SHA-256 to give the
identity. The parts are exactly those the artifact model names as identity-
bearing — its direct inputs and the identities of its Depends dependencies — and
nothing else:

1. A **version tag** for the identity scheme itself — a fixed label naming this
   construction. It lets the scheme evolve later without old and new identities
   ever colliding: change the rules, change the tag, and every identity changes
   with it.
2. The **direct inputs**, the leaf non-artifact data the artifact is built from:
   its source-tree dirhash, the target architecture, and — depending on the kind
   of artifact — the selected binary name, the build profiles and options, and
   the content hash of any pinned external bytes such as the `build-deps.yml`
   tooling. Each is a leaf value folded in directly; a locally built input
   enters not here but through the next part, as the identity of the artifact a
   Depends edge reaches.
3. The **identities of the Depends dependencies** — and only the Depends edges.
   Includes edges contribute nothing: an artifact is not built from what it
   merely carries along, so a change to an Included sibling must not change the
   includer's identity. The dependency identities are taken in a **fixed order**
   defined by the artifact's construction — the position of a dependency in the
   tuple is part of the input, so the same set of dependencies in a different
   role gives a different identity.

Each part is serialized so that no two distinct tuples can produce the same byte
stream — fields are length-delimited or otherwise framed, never simply
concatenated, so that moving a byte from the end of one field to the start of the
next cannot go unnoticed. The label contributes nothing and neither do the
Includes edges, exactly as the artifact model requires; feeding only the
identity-bearing parts in is what makes two artifacts with identical such parts
share an identity.

Because every dependency enters as its *identity* — itself the fold of its own
inputs and dependencies — the computation is recursive from the leaves up. An
artifact with no artifact dependencies folds its direct inputs alone; an artifact
higher in the graph folds its direct inputs together with identities that already
summarize entire subgraphs. One pass up the Depends graph assigns every artifact
its identity, and that is the resolve step the engine runs before consulting the
store.

## Why this gives what the engine needs

The two properties the rest of the system relies on are consequences of the
construction, not separate guarantees.

**Identical inputs give an identical identity.** Every part of the tuple is a
deterministic function of content — the dirhash of fixed-order tree entries, the
leaf values, the dependency identities computed the same way — and the fold is a
single fixed serialization hashed once. Nothing in it depends on wall-clock time,
on the machine, on checkout order, or on anything outside the enumerated inputs.
So the same inputs yield the same identity anywhere, which is what lets a build
on one machine hit the cache a build on another populated.

**Any difference gives a different identity.** A change to any leaf input, any
source byte, or any Depends dependency changes that artifact's identity, and
because dependency identities are folded in, the change rolls up through every
artifact built from it, to the image at the top. The identity is therefore the
whole dependency ledger: a cache hit is an identity already present in the store,
and since the identity captures every input exactly, the hit is exact.

## See also

- The artifact, its edges, and the resolve→lookup→build→reuse traversal that
  consumes these identities: [artifact-model.md](./artifact-model.md).
- Where source trees come from and how `3.0 (quilt)` splits committed `debian/`
  from object-store tarballs: [source-lineage.md](./source-lineage.md).
- The high-level role of content-derived identity: [ARCHITECTURE.md](./architecture.md) §2.
