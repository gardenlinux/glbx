# The Remote Cache

The object store ([object-store.md](./object-store.md)) is defined by an
interface with three backends behind it: a local store, a remote store over an
OCI registry, and a pull-through store composing the two. That document fixes the
local store and the pull-through composition in full but leaves the remote at a
sketch — *a blob is an OCI object addressed by its digest, and a map entry is
published as a tag over the identity.* This document fixes the remote: the exact
correspondence between the store's two kinds of content and an OCI registry's
primitives, why nothing published to it is lost to registry garbage collection,
and the small read-side contract the remote presents so the pull-through store
stays build-oblivious.

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
Producing results happens locally, and pushing blobs to the registry under their
`blob/<hash>` tags — and a built artifact's `map/<id>` alias — is a separate,
privileged step. This is the same import/lock separation the rest of the system
keeps, where an untrusted build never mutates shared truth
([object-store.md](./object-store.md) §The remote store). The pull-through store's
writes always go to its local member only; the registry is filled by that separate
publish step, not by building. The exact publish surface — what a producer runs,
which blobs it pushes — is beyond this document, which fixes the representation a
publisher must produce and a consumer reads: each blob under its `blob/<hash>` tag
as a one-layer manifest, and a `map/<id>` alias on the manifest-value blob for
each published artifact identity.

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
