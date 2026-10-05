# Image Assembly

This document describes how a set of locally built binary packages becomes a
finished root filesystem image — the artifact at the very top of the graph. It
builds on three earlier docs. [package-build.md](./package-build.md) explains
how each binary package is produced and validated; [artifact-model.md](./artifact-model.md)
gives the uniform artifact and the Depends/Includes edges this stage composes;
and [exec-env.md](./exec-env.md) gives the sandbox whose split between a staging
mount namespace and an inner pivoted run is what makes the layering trick below
practical.

The sketch ([ARCHITECTURE.md](./architecture.md) §6) states the shape in one
breath: take the runtime closure of a chosen set of packages, install them into
a fresh root filesystem, and pack the result deterministically — and this is
the step where the from-source guarantee is enforced. This document fixes the
*how*, and in particular answers two questions that trip people up: why an
image must not ship the tooling that builds it, and how an overlay turns that
from a surgical problem into a structural one.

## What the image is assembled from — and from what it is *not*

The first thing to be clear about: an image is assembled **exclusively from the
outputs of other artifacts in the graph** — the very same binary-package `.deb`s
that [package-build.md](./package-build.md) produces and validates. There is no
separate "image package source", no special-cased input, no distinct retrieval
path. An image artifact Depends on a set of binary-package artifacts; their
`.deb`s are its material, and that is all.

In particular, **image assembly consumes no APT repository.** It does not create
a local `file://` archive, it does not run `dpkg-scanpackages` or any other index
generator, it writes no `Packages`/`Release`/`InRelease` metadata, and it reads
no `sources.list`. Every `.deb` installed into the image comes directly out of
the content-addressed object store as a blob, selected by the graph, and is
handed to `dpkg` by path. The APT machinery that *does* exist elsewhere in glbx —
for pulling upstream index metadata when pinning tooling or importing sources —
has no part in building an image. Assembly is a function of the artifact graph
and nothing else, which is exactly what makes an image reproducible from the
graph alone.

This is also where the from-source gate closes. By the time an image is
assembled, every binary-package artifact it Depends on has already passed the
per-binary validation of [package-build.md](./package-build.md), whose locality
check requires each binary's runtime dependencies to resolve to *other locally
built binaries*. So the image's runtime closure is local by construction: there
is no point at which assembly could reach for an external mirror to satisfy a
missing dependency, because there is no mirror in the loop and nothing unresolved
is permitted to reach this stage.

## Choosing the install set

An image names the packages it wants to ship — a set of binaries, each an
output of some source build, named as `<source>:<binary>` and resolved to the
corresponding binary-package artifact. Those named binaries are the roots of the
image's content; the full set that actually gets installed is their **runtime
closure**: each named binary plus everything reachable through the Includes
edges that carry co-emitted runtime siblings (see
[artifact-model.md](./artifact-model.md)).

The closure is then **narrowed** to just the runtime population. A source build
emits binaries meant only for *building against* — development headers,
static-library packages — that have no place in a running image. Those are
dropped from the install set here: the image installs the runtime binaries of
the closure, not the build-time ones. The result is a precise, closed set of
`.deb`s, every one locally built, every one validated.

glbx decides this set — *which* packages are installed. It deliberately does
**not** decide the order in which they are unpacked and configured; that is left
to `dpkg`, for reasons the next sections make concrete.

## The tooling problem: why an image must not ship its own installer

A Debian package is not just a bag of files. Installing it runs the package's
**maintainer scripts** — `preinst`, `postinst`, and friends — small programs
the package ships to finish its own installation: creating users, compiling
caches, wiring up alternatives, registering with the init system. These scripts
assume they run inside a working Debian system. They call `dpkg`, they are
interpreted by `perl` or `mawk` or a shell, they expect the ordinary essential
userland to be present. Installing a package *correctly* therefore requires that
machinery to be available at install time.

But that machinery has no business being in the *finished* image. glbx builds an
image-centric, immutable OS: the image does not install packages in the field,
so it does not need `dpkg`, it does not need APT, and it certainly does not need
the pile of interpreters and essential packages that exist only to make
package installation work. Shipping them would be shipping the factory inside the
product — dead weight, and worse, binaries that (being essential Debian tooling)
are precisely the kind we take from a third party rather than build from source.
Leaving them in would blow a hole in the from-source guarantee.

So there is a genuine tension: the tooling must be *present while packages are
configured* and *absent from what ships*. The naïve resolution is to install
everything — tooling included — let configuration run, and then delete the
tooling afterward. That is the surgical approach, and it is a trap. You would
have to enumerate exactly which files belong to the tooling and not to anything
shipped, unpick shared files and directories, and hope no maintainer script left
the tooling entangled with the payload. It is fiddly, fragile, and never quite
certain to be complete.

## The overlay trick: presence without inclusion

The clean resolution is to arrange the filesystem so the tooling is *present*
during configuration but lives in a layer that is simply **never included** in
the final image — so there is nothing to remove afterward, because the tooling
was never part of what ships in the first place.

This is done with an **overlay filesystem** stacked from three layers:

```
Layer 2  (upper, writable)   mutations from running maintainer scripts
Layer 1  (lower, throwaway)  Debian tooling: dpkg, perl, mawk, essential userland
Layer 0  (lower, kept)       our locally built packages — the payload that ships
```

- **Layer 0 — the payload.** The narrowed install set: every `.deb` the image
  will ship, raw-extracted into a directory. These are our own built binaries
  and nothing else.
- **Layer 1 — the throwaway tooling.** The essential Debian machinery needed to
  *run* an installation — `dpkg` itself, the interpreters maintainer scripts
  need, the base userland they assume — raw-extracted into a separate directory.
  This is the only place external, not-built-from-source binaries appear, and it
  is deliberately a layer of its own. (Anything already present in Layer 0 is
  omitted from Layer 1, so the payload's own copy always wins.)
- **Layer 2 — the mutations.** An empty, writable upper directory. Everything
  that *configuration* changes — files a `postinst` creates, caches it compiles,
  ownership and permission changes, the `dpkg` status database — lands here,
  because an overlay directs all writes to its upper layer.

"Raw-extracted" is deliberate: Layers 0 and 1 are populated by unpacking each
`.deb`'s data archive straight into the directory, which places files but runs
no maintainer scripts. Scripts are run exactly once, later, by `dpkg`, so that
their effects are captured in one place — Layer 2 — rather than smeared across
the lower layers.

### Running configuration against the stack

The three layers are composed into one merged view — Layer 0 and Layer 1 as the
read-only lowers (with Layer 0 taking precedence where names collide), Layer 2 as
the writable upper. That merged view is a complete, working Debian root: it has
the payload, it has the tooling to install it, and it has somewhere to write. The
sandbox pivots into it (see [exec-env.md](./exec-env.md)) and runs the actual
installation there.

Installation is **`dpkg` directly, in two phases** — there is no APT, and nothing
reimplements `dpkg` in glbx; the real `dpkg` binary out of Layer 1 does the work:

1. **Unpack everything, deferring dependencies.** Every payload `.deb` is
   unpacked in one pass, with dependency enforcement forced off. This stages all
   files and registers all maintainer scripts without requiring any particular
   order.
2. **Configure the pending set.** A single configure pass then runs the
   maintainer scripts for everything just unpacked.

The two phases exist because the essential base has **Pre-Depends cycles** — a
knot of packages that each require another to be configured first, with no linear
order that satisfies all of them from an empty system. Unpacking everyone before
configuring anyone breaks the knot: by configure time every file in the set is
already on disk, so `dpkg` can walk the packages in a valid dependency order
regardless of the cycles. This is the same bootstrap trick a from-scratch Debian
install uses.

Note the division of labor restated concretely: **glbx chose the set; `dpkg`
chooses the configure order.** glbx hands `dpkg` the complete, closed set of
payload packages and lets it compute the topological configure order itself. glbx
does not pre-sort the packages by dependency and feed them one at a time — doing
so would duplicate, badly, logic `dpkg` already owns and would have to get the
Pre-Depends cycles right by hand. The install set is fully determined by the
graph; the ordering within it is `dpkg`'s job, and delegating it is what lets the
unmodified tooling behave exactly as it would on a normal system.

Throughout all of this, every write — the unpacked payload metadata, the files
maintainer scripts create, the populated `dpkg` database — goes to Layer 2,
because that is the overlay's upper layer. Layers 0 and 1 are untouched.

### Dropping the tooling: just don't include it

Here is the payoff. When configuration is done, the finished image is composed
from a **second** overlay view built from only two of the three layers:

```
final image  =  Layer 2 (mutations)  over  Layer 0 (payload)
                — Layer 1 is not in the stack at all —
```

Layer 1 — the entire tooling layer — is simply left out of this view. Nothing is
searched for and deleted; the tooling vanishes from the result for the plain
reason that the layer holding it was never mounted into the final composition.
The merged final view is exactly the files we built (Layer 0) plus every change
configuration produced (Layer 2), and *none* of the Debian machinery that
produced them.

This is why the overlay is so much cleaner than install-then-strip. Removal is
error-prone because it is a subtractive operation over an entangled tree:
identify every tooling file, prove nothing shipped shares it, delete carefully.
Exclusion is trivial because it is structural: the tooling and the payload were
kept in *separate layers from the start*, so excluding the tooling is a matter of
not listing one directory when composing the final view. The hard problem was
dissolved by the layout, not solved after the fact.

A subtlety worth stating, because it surprises people: a shipped package's
maintainer *scripts* can still appear on Layer 0 — they are files inside that
package's data archive, so raw extraction places them there. What is excluded is
not the inert script text but the tooling needed to *execute* it and the
*effects* of configuration, which live on the discarded Layer 1 and the retained
Layer 2 respectively. The image therefore carries no installer, even though a
few packages carry their own (now inert) installation scripts.

## Serializing the result deterministically

The final overlay view is a complete root filesystem tree. The last step turns
that tree into the image artifact's output blob — and *how* it is serialized is a
property of the chosen output format, of which there will be several. What is
not negotiable is that whatever the format, the serialization must be
**deterministic**: the same tree must always yield the same bytes, so that an
unchanged image resolves to an unchanged hash. Determinism is a requirement on
the output format, not a feature of one particular packer.

One concern is shared by every format and follows directly from the sandbox
design in [exec-env.md](./exec-env.md): **serialization runs from the staging
mount namespace, not from inside the pivoted root.** The merged tree is just a
directory visible to the orchestrating mount namespace, so the packer reads it
from outside without ever entering the chroot or the PID namespace — the one-way
"stage and harvest from outside" arrangement that doc justifies. Whatever the
format, its bytes are streamed straight into the object store, which
content-addresses them; that hash is the image artifact's result.

The rest is format-specific, and every format has its own sources of
nondeterminism to pin down. Two cases bound the range:

- **A rootfs tarball — the first and simplest format.** A tar of the tree must be
  emitted with its entries sorted by name, all timestamps zeroed, and numeric
  owners — otherwise the archive's bytes would depend on the order the kernel
  happened to enumerate the directory and on when the build ran. This is the
  format glbx produces first because it is the least machinery: no filesystem, no
  partitioning, just the tree.
- **A bootable disk image — the clear eventual direction.** A tarball is plainly
  not the whole story; the point of an image-centric OS is bootable media. Making
  a disk image reproducible is a strictly harder version of the same discipline,
  because a disk image carries metadata a tar never has, and the tooling that
  writes it loves to fill that metadata with entropy — wall-clock timestamps and
  randomly generated identifiers. Every one of those has to be forced to a value
  derived from the inputs rather than from the clock or `/dev/urandom`:
  filesystem creation timestamps and any per-filesystem UUID (for each filesystem
  written into the image), and the GPT disk GUID and per-partition GUIDs, along
  with partition geometry — all chosen deterministically. The principle is the
  same as the tar's — eliminate every input that is not a function of the build's
  own inputs — but the surface area is larger.

The common rule across all of them: because the input tree is deterministic and
the serialization is pinned to be a pure function of that tree, an unchanged
image resolves to an unchanged hash — reproducibility expressed, once again, as a
cache lookup. A format is only admissible as an image output once it meets that
bar; a packer that cannot be made to produce identical bytes from an identical
tree does not belong at the top of the graph.

## The image as an artifact

Nothing above is special to the engine. The image is one more artifact
([artifact-model.md](./artifact-model.md)): it Depends on the binary-package
artifacts whose `.deb`s it installs — so their identities fold into the image's
identity, and a change to any shipped package rebuilds the image — and its build
action is the assemble-and-pack procedure described here. The traversal that
builds any target builds an image by the same three steps as everything else;
"assemble an image" is just what this particular artifact's opaque build action
happens to do. That the top of the graph is reached by the same uniform
operation as every node beneath it is the whole point of the model.

## See also

- The binary packages this consumes, and the per-binary validation that makes
  the runtime closure local before assembly ever runs:
  [package-build.md](./package-build.md).
- The uniform artifact and the Depends/Includes edges composed here:
  [artifact-model.md](./artifact-model.md).
- The staging-vs-pivot split that lets inputs be mounted and the result packed
  from outside the chroot: [exec-env.md](./exec-env.md).
- The high-level shape this fits into: [ARCHITECTURE.md](./architecture.md) §6.
