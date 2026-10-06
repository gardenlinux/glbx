# The Remote Cache

The object store ([object-store.md](./object-store.md)) is defined by an
interface with three backends behind it: a local store, a remote store over an
OCI registry, and a pull-through store composing the two. That document fixes the
local store and the pull-through composition in full but leaves the remote at a
sketch — *a blob is an OCI object addressed by its digest, and a map entry is
published as a tag over the identity.* This document fixes the remote: the exact
correspondence between the store's two kinds of content and an OCI registry's
primitives, why nothing published to it is lost to registry garbage collection,
the small read-side contract the remote presents so the pull-through store stays
build-oblivious, and how a checkout's local content is published to the registry.

Nothing here changes the local store or the pull-through rules. The remote is one
more backend; a build run against a pull-through store cannot tell whether the
remote behind it is an OCI registry or nothing at all, except that misses turn
into hits.

## The correspondence: a tag per blob, a tag per map entry

The store holds exactly two kinds of content ([object-store.md](./object-store.md)
§Two kinds of content) — **blobs**, addressed by the SHA-256 of their bytes, and
**map entries**, keyed by an artifact identity and valued by the hash of a
manifest blob. An OCI registry offers two primitives:

- a **blob**, an opaque byte sequence addressed by its digest (`sha256:<hex>`);
- a **tag**, a mutable name resolving to the digest of an **image manifest** — a
  small JSON blob whose `config` and `layers` fields reference other blobs by
  digest. A tag is the only thing a registry keeps permanently: a blob reachable
  from no tagged manifest is garbage-collected. Tags are cheap and unlimited.

Because the only durable unit is *a tag over a manifest over its layers*, the
remote wraps **every** blob in that minimal unit. There are two tag namespaces,
one per kind of content, and they are the whole scheme:

- **`blob/<hash>`** — one per blob. For every blob the store holds, the remote
  pushes the blob and a trivial **one-layer manifest** whose single layer is that
  blob, tagged `blob/<hash>` where `<hash>` is the blob's own SHA-256. The blob's
  content address is its tag; the manifest exists only so the tag has something to
  root. This is how *any* blob — an orig tarball, a tooling `.deb`, a built
  package, a manifest blob — is stored durably, uniformly, with no notion of what
  it is or what references it. Pushing a blob and reading one back are both pure
  content addressing: `blob/<hash>` is a mechanical function of the hash.

- **`map/<id>`** — one per map entry. A map key is an artifact identity
  ([identity.md](./identity.md)), itself a SHA-256 hex string whose every
  character is a legal tag character. A map entry's value is the hash of a
  manifest blob, which — being a blob — already has its `blob/<hash>` tag and
  one-layer manifest. The map entry adds **a second tag, `map/<id>`, pointing at
  that same manifest**. It introduces no new object: `map/<id>` and
  `blob/<manifestHash>` resolve to the identical one-layer manifest; the map tag
  is simply a by-identity name for the manifest-value blob.

So a blob is self-rooting under its content hash, and a map entry is one extra
alias from an identity to a blob. Nothing is wrapped in anything larger than a
single-blob manifest; there is no closure object, no layer list enumerating other
blobs, no annotation marking roles. Both store operations become "ensure a
one-layer manifest exists and give it a tag."

### Why a manifest at all, and why only one layer

A registry will not tag a bare blob — a tag must point at a *manifest*. So the
minimum durable unit for a single blob is a manifest with exactly one layer: that
blob, the shared empty object as `config`, nothing else. The manifest carries no
information beyond "this one blob exists"; its only purpose is to be the thing a
tag can name so the registry's reachability keeps the blob. Because the manifest
for a given blob is a fixed function of that blob's digest, two tags that should
point at the same blob (a `blob/<h>` and a `map/<id>` whose value is `h`) point at
the byte-identical manifest, which the registry stores once.

### Resolving a map entry is tag lookups and blob reads

Resolving a map entry needs no bespoke operation and no manifest parsing by the
registry. On a local map miss the pull-through store ([object-store.md](./object-store.md)
§The pull-through store):

1. resolves the **`map/<id>` tag** to its one-layer manifest and reads the single
   layer's digest — that *is* the manifest blob's hash, the map value;
2. pulls that **manifest blob** by hash (its own `blob/<hash>`) into the local
   store, and reads it to recover the output `(hash, name)` list
   ([artifact-model.md](./artifact-model.md));
3. pulls each **output blob** by hash (each its own `blob/<hash>`) into the local
   store;
4. records the local map entry pointing at the now-present manifest blob.

Every step is "resolve a tag to its one layer's digest, fetch that blob" — the
same two moves whether the tag is a `map/<id>` or a `blob/<hash>`. The question of
*which* blobs an entry names is answered by the pull-through store reading the
manifest-blob format it already owns; the registry never parses it. The miss
becomes a permanent local hit, manifest and outputs included, exactly as the
pull-through rules require.

## The read contract the remote presents

Everything above needs only two things from the remote, and these are the whole
of the remote's read side:

- **open a blob by hash** — resolve the blob's `blob/<hash>` tag to its one-layer
  manifest, read the single layer's digest, and serve those bytes; an absent tag
  or blob is a miss. This serves output blobs, manifest blobs, and input blobs
  identically — the remote does not distinguish them.
- **resolve an identity to its map-value hash** — resolve the `map/<id>` tag to
  its one-layer manifest and return that layer's digest, which is the manifest
  blob's hash; a missing tag is a miss.

Both are the same move — resolve a tag, read its single layer's digest — differing
only in which namespace the tag is in. The remote serves blobs by hash and
resolves two kinds of tag to a hash; the pull-through store does the closure walk,
using the identity resolution to find a starting hash and blob reads to pull
everything it reaches. The remote parses no manifest-blob bytes, knows nothing of
outputs, leaves, Debian packages, or builds, and holds no build-system concept
whatever — it is a content-addressed blob service with a by-identity alias on top.
This is what keeps the backend symmetric with the local store at the interface and
oblivious to everything above it.

A map key that is not a published identity — the InRelease fetch cache keys its
blob under a map entry too ([repo access](../impl/repo.md)), and that entry is a
local network cache, never published — simply has no tag on the registry. Its
resolution misses remotely and falls back to the local store, the ordinary way a
true miss is reported. Only artifact-identity entries are ever published; the
remote has a tag for exactly those.

## Why nothing published is garbage-collected

An OCI registry reclaims any blob not reachable from a tagged manifest. A scheme
that pushed blobs and left them bare would see them collected out from under it.
This scheme never leaves a blob bare, because the unit of publishing *is* a blob
plus the tagged one-layer manifest that roots it:

- **Every published blob is rooted by its own `blob/<hash>` tag.** The blob is the
  single layer of a one-layer manifest, and that manifest is tagged. Because a
  manifest structurally references its config and its layer by digest, the
  registry's own reachability keeps the blob alive for as long as its tag exists.
  A blob's lifetime on the registry is exactly its tag's lifetime — no blob
  depends on being enumerated inside some *other* object's layer list to survive.
- **Output blobs are rooted the same way as any other blob.** An artifact's
  manifest blob and each output blob it names are separate blobs, each with its
  own `blob/<hash>` tag. The `map/<id>` tag roots the manifest blob (redundantly
  with its `blob/<hash>` tag); the output blobs are kept alive by *their own* tags,
  not by appearing as layers of the map entry. There is no closure object whose
  loss could orphan them and nothing to enumerate at publish time beyond the blobs
  themselves.
- **The shared empty config is kept the same way.** Every one-layer manifest names
  the same two-byte empty object as its config, so it is referenced by every tag
  and never dangles.
- **Each blob and each identity is its own root.** Every tag is independent; there
  is no aggregating index whose loss would orphan the rest. An identity is
  per-architecture already ([identity.md](./identity.md)), so one `map/<id>` tag
  resolves to one manifest. Enumerating what is published is listing the tags in
  the two namespaces.

Reclaiming space on the registry is therefore granular and mirrors local GC:
dropping a blob is deleting its `blob/<hash>` tag (and any `map/<id>` alias), after
which the registry's own GC reclaims the now-unreferenced blob. Locally a blob is
kept by being in the engine's keep-set ([object-store.md](./object-store.md)
§Garbage collection); remotely a blob is kept by having a tag. Both reduce to a
per-blob keep decision, and a map entry stays valid exactly while the blobs it
names still have their tags.

## Every blob is storable — inputs included

Because the unit of publishing is a single blob under its own `blob/<hash>` tag,
there is no blob the registry cannot hold. This matters most for the blobs that
belong to no artifact's output manifest: an upstream `orig.tar` or a tooling
`.deb` referenced only by a `sources.yml` / `build-deps.yml` entry
([source-lineage.md](./source-lineage.md), [package-build.md](./package-build.md)).
These are *inputs*, never listed among a build's outputs, and a registry that
could not serve a build's inputs would be useless to a cold consumer. Here an
input is a blob like any other: pushed under its content hash, tagged
`blob/<hash>`, pulled back by hash. The registry is a **full content mirror**,
able to serve every blob a build reads — inputs, intermediate manifests, and
outputs alike — not merely a cache of finished outputs.

Remote retention is per-blob: a blob is kept on the registry exactly while it
has a tag, so there is no protected-root set to maintain. The git tree's
`sources.yml` / `build-deps.yml` still record each input by hash and retrieval
URL, so a cold machine with no registry — or a blob the registry happens not to
have — still obtains inputs the pure-cache way: `restore-cache` fetches from the
recorded URLs and verifies against the recorded hash. The registry mirror and
the recorded URLs are two independent sources for the same content-addressed
bytes; a consumer uses whichever answers, and either way the hash it got is the
hash it asked for.

## Publishing

Reads are the hot path and are transparent; writing to the registry is neither.
Producing results happens locally, and filling the registry is a separate,
privileged step — the same import/lock separation the rest of the system keeps,
where an untrusted build never mutates shared truth ([object-store.md](./object-store.md)
§The remote store). The pull-through store's writes always go to its local member
only; the registry is filled by this step, never by building.

Publishing takes the current checked-out conf-dir and an architecture, and makes
the registry hold everything that checkout's state references and that was built
or fetched locally. It is deliberately a *mirror* operation — enumerate the local
hashes that matter, see which the registry lacks, upload the gap — with no
bookkeeping of its own. It reuses the two enumerations the system already performs
for other reasons; publishing is their union pointed at the registry.

### What to publish: the two enumerations

**The recorded inputs.** Exactly the set `restore-cache` collects
([object-store.md](./object-store.md)) — every blob hash named in a package's
`sources.yml`, in each `build-deps.yml`, and in the root `rootfs-deps.yml`,
arch-filtered the same way. These are the upstream archives and tooling `.deb`s a
build consumes. Each is published as a blob under its `blob/<hash>` tag; an input
is not a map entry and gets no `map/<id>`.

**The built outputs.** Exactly the set garbage collection walks
([object-store.md](./object-store.md) §Garbage collection) — the build graph of
the checkout for the target architecture, and for each node that has already been
built, its map entry resolved to a manifest-blob hash and the output-blob hashes
that manifest names. A node not yet built contributes nothing, exactly as it
contributes nothing to the GC keep-set. For each built node, publishing emits:
the output blobs and the manifest blob each under their `blob/<hash>` tag, and the
node's identity as a `map/<id>` alias on the manifest blob.

Both enumerations yield the same currency — content hashes, plus the handful of
identities that get a map alias — so they merge into one deduplicated worklist: a
set of blob hashes to ensure, and a set of `(identity, manifest-hash)` pairs to
alias. A hash that is only in the local store because some *other* checkout built
it, and that this checkout's graph does not reach, is simply not enumerated;
publishing mirrors what the current checkout accounts for, nothing more.

### The upload: check what exists, push the rest

Publishing a blob is idempotent and content-checked, so the step is a diff, not a
blind re-upload:

1. **Skip blobs the registry already has.** For each blob hash on the worklist,
   test whether its `blob/<hash>` tag already resolves. Because the tag is a pure
   function of the content, an existing tag means the exact bytes are already
   published — nothing to do. Only the gap is uploaded.
2. **Upload a missing blob as blob + one-layer manifest + tag.** Push the blob's
   bytes, then its one-layer manifest, then the `blob/<hash>` tag — the minimal
   durable unit from §The correspondence. A blob absent from the *local* store is
   skipped with a note rather than failing the run: publishing can only mirror
   what the local store holds, exactly as `restore-cache` can only check what it
   can fetch.
3. **Alias each built identity.** For each `(identity, manifest-hash)` pair, if
   `map/<id>` does not already resolve, set it to point at the same one-layer
   manifest `blob/<manifest-hash>` already names. This introduces no object; it is
   one more tag on an existing manifest.

The shared empty config and any manifest a prior push already created are likewise
skipped when present. The result is that running publish twice over an unchanged
checkout uploads nothing the second time, and running it after one more package is
built uploads only that package's new blobs and alias.

### Why this stays simple

Publishing invents no new reachability model and no new on-registry shape. It
produces exactly the layout §The correspondence fixes — blobs under `blob/<hash>`,
built identities under `map/<id>` — using the input enumeration `restore-cache`
already defines and the output enumeration GC already defines. It keeps no state:
the registry's own tags are the record of what is published, so the "what already
exists" check is a tag lookup, not a local ledger. And it is safe to interrupt and
rerun, because every step is an idempotent "ensure this tag exists." The producer
surface is therefore a single read-only-against-local, write-against-remote pass;
what command exposes it, and the registry endpoint it targets, follow the same
`--conf-dir` / `--arch` / registry-reference conventions as the rest of the
command line.

## See also

- The store's two kinds of content, the pull-through rules this remote plugs
  into, and local garbage collection it mirrors:
  [object-store.md](./object-store.md).
- Why an identity is a SHA-256 hex string usable verbatim as a tag, and why it is
  per-architecture: [identity.md](./identity.md).
- What a manifest blob lists and the resolve→lookup→build→reuse traversal that
  reads these entries: [artifact-model.md](./artifact-model.md).
- The reclaimable inputs recorded by hash and URL — storable on the registry
  under their own `blob/<hash>` tags, and otherwise re-fetched from their URLs:
  [source-lineage.md](./source-lineage.md), [package-build.md](./package-build.md).
