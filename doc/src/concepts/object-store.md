# The Object Store

The object store is where results persist between builds. The architecture
sketch ([ARCHITECTURE.md](./architecture.md) §3) names it as the system's one
place where anything is kept: everything the build might reuse — upstream
archives, built `.deb`s, assembled images, the manifests that tie an artifact's
outputs together — is a blob here, and everything else is derived from those.
The artifact model ([artifact-model.md](./artifact-model.md)) leans on it from
the other side: the engine's whole cache check is a lookup in this store by
identity, and a build's result is written back here under the identity that
produced it.

The store is a **pure cache**, and nothing in it is irreplaceable. Every blob is
either a build output — reconstructible by rerunning the build its identity names
— or an external input recorded elsewhere with both a content hash and the
locations to fetch it from ([source-lineage.md](./source-lineage.md),
[package-build.md](./package-build.md)). So any blob the store drops can be
produced again: a rebuilt output comes back identical because its inputs are
unchanged, and a discarded input is re-fetched from its recorded source and
verified against its recorded hash. This is the property that keeps the store
simple — there is no class of blob the store must defend, no permanent state to
protect, nothing that a too-eager cleanup can lose for good.

This document fixes what the store is — its two kinds of content, its on-disk
shape, the interface a store presents, the local and remote backends, the
pull-through cache that composes them, and how garbage collection reclaims
space.

## Two kinds of content

The store holds two distinct things, and keeping them separate is what makes it
small:

- **Blobs** — immutable byte sequences addressed by the SHA-256 of their
  content ([identity.md](./identity.md)). A blob's address *is* the hash of its
  bytes, so a blob cannot change under a fixed address and storing the same
  bytes twice is the same blob. This is the content-addressed store proper.
- **Map entries** — a mapping from an artifact **identity** to the blob that
  records its result. Where a blob is addressed by *what it contains*, a map
  entry is keyed by *what produced it*: the content-derived identity the engine
  computes before building. The value under a key is the hash of a **manifest**
  blob — a small blob listing the artifact's named outputs, one `(blob hash,
  output name)` pair per line, exactly the manifest the artifact model
  describes.

A lookup is therefore two steps: resolve an identity through the map to a
manifest blob, then read that blob to recover the named output blobs. Both steps
are hash-keyed reads; neither needs a directory listing or an index.

Blobs and map entries also differ in how they relate during cleanup, which
§Garbage collection builds on. A map entry's value always points at blobs, so
the map is derived from the blobs: an entry is meaningful only while the blobs it
names are present. Blobs are the ground truth; the map is a convenience layer
over them.

## On-disk layout

A local store is a single directory tree with two subdirectories:

```
<root>/
├── blobs/
│   ├── 0e/
│   │   ├── 8c40f9…c34a        ← content-addressed file, name = SHA-256 minus prefix
│   │   └── …
│   └── …
└── map/
    ├── 4d/
    │   └── 7728…3e0e          ← tiny file: the hex hash of a manifest blob
    └── …
```

Both `blobs/` and `map/` sit under the same rule: an entry is **sharded** by the
first two hex characters of its key, and the filename is the remaining 62. A
blob with hash `0e8c40f9…c34a` lives at `blobs/0e/8c40f9…c34a`; a map entry for
identity `4d7728…3e0e` lives at `map/4d/7728…3e0e`. The two-character shard caps
any one directory at 256 subdirectories, keeping directories small without
anyone ever having to list the whole store to find a path — the path is a pure
function of the key.

A blob is written **atomically**: the bytes are streamed to a temporary file in
the store while the SHA-256 is computed on the side, and only once the full
digest is known is the file renamed into its content-addressed place. A reader
therefore never sees a partial blob under a valid address, and two writers of
the same content converge on the same final path harmlessly. A map entry is a
one-line file written the same temp-then-rename way, so updating a key is atomic
per key and there is no shared index to serialize on.

There is no third directory, and in particular nothing that marks a blob as
permanent. One might expect a store to need a set of *pins* — named roots that
protect certain blobs (freshly fetched upstream archives, resolved tooling
`.deb`s) from collection on the grounds that they are inputs nothing rebuilds.
This store has none, and deliberately so: those inputs are not irreplaceable
here. Each is recorded with its content hash **and** its retrieval locations in
the git tree (`sources.yml`, `build-deps.yml`), so a collected input is simply
re-fetched and re-verified on next use, exactly like a collected output is
rebuilt. A pin would protect bytes on one machine's disk while guaranteeing
nothing for any other machine, and it would be a standing leak — a root left
behind by a long-ago build keeps bytes alive forever until someone remembers to
remove it. Making the store a pure cache removes that failure mode outright: the
only thing that keeps
a blob is being wanted *now*.

## The store interface

Everything above describes a *local* store, but the system also wants a shared
remote store and a way to compose the two. So the store is defined by an
**interface**, and the concrete forms implement it. The engine is written
against the interface alone and never against a particular backend.

The interface is the small set of operations the engine actually performs:

- **has / open a blob by hash** — test presence, and read its bytes.
- **store a blob** — write bytes, returning the content hash (which the store
  verifies by hashing what it wrote).
- **path of a blob** — the filesystem path of a blob's bytes, for the consumers
  that must hand a real path to an external tool (a bind-mount into the sandbox,
  `dpkg-deb` reading a `.deb`) rather than an open stream.
- **get / set a map entry** — resolve an identity to its manifest-blob hash, and
  record that mapping.
- **garbage-collect against a keep-set** — reclaim every blob not in a set the
  caller supplies (see §Garbage collection).

Three things implement this interface: a **local** store backed by the directory
tree above, a **remote** store backed by an OCI registry, and a **pull-through**
store that composes a local and a remote one. The engine treats all three
identically; the differences are entirely in what each does behind the
interface.

## The local store

The local store is the directory tree described under §On-disk layout, and it is
the only backend that implements every operation fully. Blobs and map entries
are read and written as files; `path` returns a real path into `blobs/`; GC walks
`blobs/` and deletes. It is the ground on
which the other two backends stand: the remote has no local path to offer, and
the pull-through cache keeps all of its persistent state in exactly one local
store. Opening a local store resolves its root to an absolute path, because the
store is consulted from inside a sandbox whose working directory is not the
host's.

## The remote store

The remote store presents the same blob and map reads over an **OCI registry**,
so that outputs built once — in CI, typically — can be fetched by a developer's
machine instead of rebuilt. A blob is an OCI object addressed by its digest,
which is the same SHA-256 the rest of the system already uses, so "pull a blob
by hash" is a direct registry fetch and the bytes verify themselves against the
digest that requested them. An artifact's map entry is published as a tag over
the identity, resolving to the artifact's manifest and its output blobs.

The remote is deliberately **read-only and path-less** from the engine's side.
It implements blob reads and map reads and nothing more; the write-side and
path-side operations of the interface are *not* available on a bare remote and
fail if called:

- **Writing** a blob or setting a map entry on the remote directly is refused.
  The engine never builds "into" the registry — results are produced locally and
  publishing them to the registry is a separate, privileged step, not something
  that happens on the build hot path. (Keeping publication out of the ordinary
  build path mirrors the import/lock separation elsewhere: the untrusted build
  never mutates shared truth.)
- **Path** has no meaning on the remote: there is no local file to point at. A
  remote blob must first be materialized locally before any path exists for it,
  which is precisely what the pull-through store does.

A bare remote is thus only useful wrapped in a pull-through store; on its own it
can answer "do you have this, and what are its bytes" but cannot satisfy the
consumers that need a path, and cannot be built into.

## The pull-through store

The pull-through store is the one the engine normally runs against. It is
**constructed from two stores given as the interface** — a local store and a
remote store — and composes them so that a miss in the local one is satisfied
from the remote and *becomes* a local hit for next time. It holds no storage of
its own: everything it persists, it persists by writing into its local member.

Its defining rule is that **a remote result is never forwarded to the caller
directly**. On a read it first consults the local store; on a local hit it
returns that and never touches the remote. On a local miss, instead of returning
the remote's answer, it:

1. fetches the bytes from the remote,
2. **writes them into the local store** — which re-hashes on write and so
   verifies the content against the hash that was asked for, and
3. **re-issues the same read against the local store**, returning *that* result
   to the caller.

So the value the caller receives always comes from the local store, whether the
blob was there to begin with or was just pulled. A pull is a cache fill as a side
effect of a read: the second time the same blob is read, it is a plain local hit
and the remote is not consulted. This is what lets a reclaimed input or a
CI-built output be restored transparently by the act of using it.

The same shape applies to the operations that are not plain reads:

- **Path** on the pull-through store must return a *local* path, so a path
  request for a blob absent locally first pulls it from the remote into the local
  store (identically to a read) and then returns the now-present local path. A
  consumer that needs a real file — the sandbox bind-mount, `dpkg-deb` — gets one
  whether or not the blob was cached, even though the bare remote has no path to
  give.
- **Map lookup** falls through the same way: a local map miss pulls the manifest
  and each output blob it names from the remote into the local store, records the
  map entry locally, and then resolves it from the local store. The miss is
  turned into a permanent local hit, manifest and outputs included.
- **Writes** — storing a blob and setting a map entry — go to the **local**
  member only. The pull-through store is a cache in front of a shared
  read-only remote, not a way to write to it; publishing to the remote remains
  the separate privileged step. A genuine remote miss (the blob or tag is absent
  there too) surfaces as the ordinary local not-found, so a miss is reported the
  same way with or without a remote behind it.

A plain local store with no remote and a pull-through store whose remote happens
to have nothing behave identically on every miss — the remote only ever adds
hits, never changes how a true miss looks.

## Garbage collection

The store grows without bound as builds accumulate outputs and inputs; garbage
collection reclaims what is no longer wanted. The design keeps the store itself
free of any *policy* about what is worth keeping: deciding that is the engine's
job, because only the engine knows which artifacts the current checkout's graph
reaches. So GC takes a **keep-set computed by the caller** and reclaims
everything outside it.

### The store does not compute the keep-set

The engine computes the keep-set before calling GC, by whatever mechanism it
likes — typically graph reachability from the current checkout. The store is
handed the finished set and does not re-derive it; it never inspects the map to
decide what to keep on its own. This keeps the store ignorant of artifacts,
graphs, and reachability — it knows only blobs, map entries, and the one set it
was given.

Because the store protects nothing of its own, the keep-set is the *entire*
account of what survives a collection. There is no second category — no pinned
roots, no permanent inputs — layered underneath it. A blob the engine does not
place in the keep-set is reclaimed, and that is safe for every blob precisely
because the store is a pure cache: a reclaimed output is rebuilt and a reclaimed
input is re-fetched from its recorded source, both verified by hash. The engine
is free to compute a tight keep-set (only what the current graph needs) or a
generous one (recently used artifacts too, as a cache-warmth policy) — that
tradeoff lives in the engine, not the store.

### The keep-set names blobs, and the map follows the blobs

The keep-set specifies **blobs only**. This is deliberate and it is what makes GC
trivial, because of the layering established above: the map is derived from the
blobs — a map entry is meaningful only while the blobs it points at exist. So GC
is two sweeps, blobs first and the map second, the map following the blobs:

1. **Sweep blobs against the keep-set.** Delete every blob in `blobs/` not named
   in the keep-set; keep the rest. This is the whole of the retention decision —
   a single set-membership test per blob, with no reference back to the map.
2. **Sweep the map to follow the blobs.** Delete every map entry whose value now
   points at a blob that no longer exists; keep the rest. The map is not given
   its own keep-set — an entry's fate is dictated entirely by whether its target
   blob survived step 1.

Because the map is swept to match the blobs rather than against an independent
policy, a map entry can never outlive its blob, and the caller never has to
enumerate map entries to protect them — it keeps the *blobs* an artifact
produced (its manifest blob and the output blobs that manifest names), and the
entry pointing at them is kept automatically by step 2. Keep the blobs an
artifact's manifest references and its map entry stays valid; omit them and the
entry is swept away as dead. There is no state in which the map claims an
identity is built but its bytes are gone.

## Why this design

The whole store is a filesystem tree plus a thin interface. There is no database,
no embedded key-value store, no bespoke on-disk format — a blob is a file named
by its hash, a map entry is a one-line file. That buys several properties for
free:

- **Reproducibility is a lookup.** Because a map key is the content-derived
  identity and the identity captures every input exactly, a cache hit is an
  identity already present — equivalent, not merely probable. There is no
  stale-cache failure mode to guard against.
- **The remote is the same shape as the local.** A content-addressed tree maps
  onto an OCI registry's digest-addressed objects directly, so "pull by hash"
  needs no translation and the pulled bytes verify themselves. Sharing a store is
  distributing already-built outputs, never a precondition for building.
- **Everything is reclaimable, so GC is just a cache eviction.** No blob is
  irreplaceable — outputs rebuild, inputs re-fetch from recorded sources — so
  there is nothing to protect from collection, no roots to track, and no standing
  leak from a protection left behind. Retention policy is the engine's to choose
  and the store is a pure cache under it.
- **GC is two set-sweeps.** Feeding the store a finished blob keep-set reduces
  collection to a membership test over blobs and a follow-the-blobs pass over the
  map — no graph walking inside the store, no way to leave the map inconsistent
  with the blobs.

## See also

- What an identity and a manifest are, and the resolve→lookup→build→reuse
  traversal that reads and writes this store: [artifact-model.md](./artifact-model.md).
- The SHA-256 scheme that addresses blobs and keys the map:
  [identity.md](./identity.md).
- The upstream archives recorded in `sources.yml` by hash and retrieval
  location — reclaimable inputs, re-fetched on demand:
  [source-lineage.md](./source-lineage.md).
- The external tooling `.deb`s recorded in `build-deps.yml` the same way:
  [package-build.md](./package-build.md).
- The store's role as the system's cache of results and its intended remote
  form: [ARCHITECTURE.md](./architecture.md) §3.
