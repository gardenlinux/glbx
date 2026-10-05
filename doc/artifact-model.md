# Artifact Model

This document defines the one abstraction the whole build engine is built on:
the **artifact**. The architecture sketch
([ARCHITECTURE.md](./ARCHITECTURE.md) §1–§2) states the central idea — every
build output is an artifact with an identity derived from its inputs, looked up
in a content-addressed store and either reused or built. [package-build.md](./package-build.md)
already works in these terms, modelling a source build and a binary package as
two *kinds* of artifact with edges between them. This document fixes what sits
underneath both: what an artifact *is* as a uniform object, what kinds of edges
connect artifacts, how identity rolls up through its Depends edges, and how the
engine walks a graph of them to build a target.

It stays at the level of the model and the traversal. The exact rules for
hashing a source tree and folding inputs into an identity are a separate
concern with their own document; here identity is used as a well-defined value
without pinning down how its bytes are computed. Likewise *where* a built result
is stored on disk is the object store's concern; this document only needs that a
store exists which maps an identity to the result built under it.

## What an artifact is

An **artifact** is the smallest unit the engine builds, caches, and depends on.
It is not a thing on disk; it is a *description* of a build — enough to be found
in the store by identity and, on a miss, to run the build that produces a
result. To the engine, every artifact — whatever it ultimately builds — is the
same handful of things:

- an **identity** — the content-derived hash the store and the cache key on;
- a **label** — a human-readable name, used when constructing and inspecting the
  graph and in diagnostics; it has no bearing on identity or caching;
- its **dependencies** — the other artifacts it relates to, each reached by a
  typed edge (below); the edge type says whether it is built from that artifact
  or merely carries it in a closure;
- a **build action** — the opaque step the engine runs on a cache miss to
  produce the artifact's result, given its dependencies' results.

The engine treats every artifact only through these. It has no built-in concept
of "a package" or "an image", and it does not dispatch on what an artifact is:
the build action is opaque to it, so the only thing it ever does with an
artifact is resolve its identity, consult the store, and — on a miss — assemble
its dependencies and invoke its build action. That the system builds source
packages, binary packages, and images is a fact about which code constructs the
artifacts and what their build actions do; it is invisible to the traversal.
This is what lets one traversal drive the entire system.

An artifact's result is a **manifest**: the set of blobs the build produced,
named within the artifact. A source build's manifest is every `.deb` it emitted;
a binary package's manifest is the single `.deb` it selected and re-emitted; an
image's manifest is the assembled rootfs or disk image. A dependent does not
take "the result" wholesale — it names which part of a dependency's manifest it
consumes, which is what the edge records.

## The dependency edges

An artifact can relate to another artifact in two ways, and they are genuinely
different relationships — not two settings of one knob:

- **Depends** — "I am built from B."
- **Includes** — "I carry B along for whoever uses me."

### Depends

If an artifact is *built from* another, three things are true at once, because
they are the same fact seen from three sides: B must be built first; B's exact
output is what this artifact's build action consumes; and B's identity is part
of this artifact's identity, so a change to B rebuilds it. Depends edges must be
acyclic (nothing is built from itself), and because every rebuild cascades along
them, keeping them few is what keeps the graph shallow.

Both of [package-build.md](./package-build.md)'s build relations are Depends. A
source build's `build_depends` names binaries it needs in its chroot to compile,
so changing one forces a recompile. And a binary-package artifact is built from
one named `.deb` of a source build — it selects that output, waits on that
build, and takes that build's identity into its own.

### Includes

If an artifact merely *carries another along*, none of the above applies. The
relationship is instead about consumers:

> If `A` Includes `B`, then anything that Depends on `A` also depends on `B`.

Includes does not order `A` against `B`, and `B`'s identity does not enter `A`'s
identity — `A` is not built from `B`. Since it imposes no build order, Includes
edges may form cycles, and the scheduler and the identity computation both
ignore them.

It exists for one case: sibling binaries co-emitted by one source build whose
runtime dependencies name each other (two libraries from a single
`dpkg-buildpackage` run, cross-referenced via `${shlibs:Depends}`). Neither is
built from the other, so Depends would be both false and a forbidden cycle — but
a consumer of one needs the other present at runtime. Each sibling Includes the
other, and any consumer of either drags in both. This is
[package-build.md](./package-build.md)'s `runtime_depends`, and it is what the
binary-package stage walks to compute a binary's full runtime closure.

For example, the `openssl` source emits both `libssl3t64` and
`openssl-provider-legacy` from one build, and each one's `${shlibs:Depends}`
names the other. So `libssl3t64` Includes `openssl-provider-legacy` and vice
versa, and any binary that Depends on `libssl3t64` pulls `openssl-provider-legacy`
into its runtime closure too — without either sibling waiting on the other to
build.

## What determines an artifact's identity

The engine never looks inside an artifact to compute its identity; the artifact
produces its own, by folding together two things:

- its **direct inputs** — the leaf, non-artifact data it is built from: source
  tree, target architecture, selected binary name, build profiles and options,
  and any pinned external bytes. The `build-deps.yml` tooling enters here, by
  content hash — it is leaf data, not an artifact in the graph. A locally built
  binary, by contrast, *is* an artifact and so is reached by a Depends edge, not
  inlined here;
- the **identities of its Depends dependencies** — and only those, since
  identity records what an artifact is built from (Includes carries no build
  input and can cycle).

The fold is deterministic: identical inputs and identical Depends identities
give an identical artifact identity, and any difference anywhere gives a
different one. The label and the Includes edges contribute nothing.

Two consequences matter. First, identity is a function of *dependency
identities*, not their results, so it can be computed by walking the Depends
graph from the leaves up before anything is built — the identity is known first,
the result produced only on a cache miss. Second, the identity *is* the
dependency ledger: a change to any leaf input changes that artifact's identity
and, through the Depends graph, every artifact built from it, up to the image.
No separate change-tracking database exists and there is no stale-cache failure
mode — a cache hit is simply an identity already present in the store, and since
the identity captures every input, the hit is exact.

This is why a locally built input is "pinned by construction": its identity
rolls up from its own source and inputs, so a Depends edge to it pins its exact
bytes with no separate lockfile — hence [package-build.md](./package-build.md)
pins only *external* `.deb`s in `build-deps.yml` and leaves locally built inputs
to the graph.

> How an identity's bytes are actually computed — how a source tree is hashed,
> how the direct inputs and dependency identities are ordered and folded — is
> defined in its own document. This one needs only that identity is a
> deterministic, collision-resistant value over exactly those parts.

## Building a target: the engine traversal

Building any target is one general operation, the same for every artifact:

1. **Resolve identity.** Walk the target's Depends graph to its leaves and fold
   identities upward, so every artifact in the graph has a known identity. This
   follows Depends edges only — identity depends on nothing else — and needs no
   building and touches no sandbox.
2. **Consult the store.** For each artifact, a hit — its identity is present —
   means its result is already available and its subgraph need not be visited for
   building at all. The store lookup is the whole of cache checking; there is no
   separate validity test.
3. **Build on a miss.** For an artifact whose identity is absent, first ensure
   the results it is built from are present — the artifacts reached by its
   Depends edges, plus, by the closure rule, everything those dependencies
   Include — recursing into the same three steps. Then run the artifact's build
   action inside the sandbox with those results available, and store the produced
   manifest under the artifact's identity.
4. **Reuse the result.** Whether hit or freshly built, the artifact's manifest is
   now in the store under its identity, available to its dependents by the same
   lookup.

Because step 2 can prune an entire subgraph on a hit, a target whose inputs are
unchanged resolves to a walk of identities and a set of store lookups, with no
build work — reproducibility expressed as a cache lookup, as the vision
requires. And because only Depends edges fold into identity, a change low in the
graph rebuilds exactly the artifacts that are *built from* it, directly or
transitively, and nothing else — an Includes edge propagates a runtime closure
to consumers but never, on its own, triggers a rebuild.

The engine's indifference to what an artifact builds is what makes this
uniform. Introducing a new sort of artifact — a source build, a binary package,
an image — means writing code that constructs it with the right inputs, edges,
and build action; the traversal above does not change and gains no new case. The
engine walks a graph mixing all of them without ever distinguishing them: the
build action it invokes is opaque, so from its vantage there is only ever "an
artifact".

## See also

- The high-level shape these pieces fit into: [ARCHITECTURE.md](./ARCHITECTURE.md).
- The source-build and binary-package artifacts, and their `build_depends` /
  `runtime_depends` edges, in concrete terms: [package-build.md](./package-build.md).
- Where an artifact's source tree comes from: [source-lineage.md](./source-lineage.md).
