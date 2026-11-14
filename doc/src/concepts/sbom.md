# SBOMs

glbx emits an SPDX software bill of materials for every artifact result it
builds. There is **one SPDX document per artifact result in a selected SBOM
namespace**, and that result is identified by the hash of its
[output manifest](./artifact-model.md#the-output-manifest). A source build that
emits ten Debian binary packages produces one SPDX document with ten named
package outputs; binary validation, rootfs assembly, image assembly and image
tooling builds each describe their own artifact result the same way.

Each document describes one glbx build and its outputs. Sources and the artifact
`Depends` inputs it was built from get `hasInput` edges; every package making up
the build chroot gets a direct `usesTool` edge; each package named in an emitted
binary's `Built-Using` or `Static-Built-Using` field gets a `hasStaticLink` edge
from that binary. A package can play more than one role, and for any role its
*origin* decides its SPDX shape: a lockfile-provided package is described locally,
while a glbx-provided one references the producing SPDX element in that producer's
own document.

The documents are retrieved two ways. A glbx consumer pulls an individual SPDX
document through the [object store](./object-store.md), exactly as it pulls any
other blob. A consumer with only ORAS pulls the OCI tag
`sbom-<output-manifest-hash>` and gets the root SPDX document together with every
document it imports, directly or transitively — the **SPDX closure** — in one
call. Both paths reference the same underlying bytes.

This chapter fixes the addressing, the SPDX graph glbx emits, how one document
pins the producer documents it imports, how each artifact kind maps onto SPDX,
and the object-store and OCI retrieval contracts. The format is SPDX 3.0.1
JSON-LD throughout, serialized with the official context under a single `@graph`,
using the Core, Software and Build profiles.

## Addresses and terminology

Several distinct hashes travel together in this chapter, and keeping them apart
is the whole of understanding the addressing. A document checksum is not an
output-manifest hash, and neither is the OCI manifest digest that an OCI tag
resolves to.

| Name | What it identifies |
| --- | --- |
| **Output-manifest hash** `M` | The SHA-256 of an artifact's [output manifest](./artifact-model.md#the-output-manifest), which lists the named outputs and their object-store blob hashes. `N` denotes the same kind of hash for a *producer* artifact. |
| **SPDX document checksum** `D` | The SHA-256 of one serialized SPDX document. Locally it is the object-store blob hash of that document; after publication, `sha256:<D>` is the OCI blob digest of the same bytes. |
| **Payload checksum** | The checksum of a named artifact output — e.g. the whole `fixture.deb` archive. The corresponding SPDX output element records this checksum. |
| **SBOM map key** `K(A,M)` | The object-store map key folded from a dedicated SBOM seed, the selected SBOM namespace `A`, and the output-manifest hash `M`. The map entry under it points at `D`. |
| **OCI manifest digest** | The digest of an OCI manifest's own bytes. An OCI tag such as `sbom-<M>` resolves to an OCI manifest digest; the `M` in that tag names the artifact result. |

The SBOM namespace `A` is set with `GLBX_SBOM_NAMESPACE` and defaults to
`localhost`; Garden Linux CI uses `gardenlinux.io`. The same output manifest can
carry a local SPDX document and a CI SPDX document at once, each under its own
SBOM map entry.

SPDX element IDs are minted under the prefix `urn:spdx:<A>/glbx/sboms/<M>`,
abbreviated `B(A,M)` below, with the element path appended — so CI's Debian
package element is
`urn:spdx:gardenlinux.io/glbx/sboms/<M>/outputs/fixture.deb`. The exported file
is named `<M>.spdx.json`, and a reader follows a reference by matching the full
SPDX element ID inside that file. These URNs are glbx identifiers: the `spdx`
URN namespace has no IANA registration and no SPDX-provided resolver.

## The SPDX graph for one artifact

A reader enters a document at its SPDX `SpdxDocument`. Its `rootElement` selects
the SPDX SBOM, whose `rootElement` selects the logical artifact result. Following
`contains` from that result reaches the deliverables; from a deliverable,
following incoming `hasOutput` reaches the producing build, and from the build,
`hasInput` and `usesTool` reach its materials and tools.

The bytes are SPDX 3.0.1 JSON-LD with the official context, one `@graph`, and the
Core, Software and Build profiles. For the same evidence,
creation metadata and producer-document pins, glbx emits byte-reproducible SPDX:
SPDX imports and OCI layer descriptors refer to the exact bytes first stored.

### SPDX elements

The paths below are written relative to `B(A,M)/` for readability; the emitted
IDs and references use the full URI.

| Element path | SPDX type and meaning |
| --- | --- |
| `document` | `SpdxDocument`. Its `element` lists the locally defined elements other than itself; its `import` lists the foreign elements this document uses. |
| `sbom` | `software_Sbom` with `software_sbomType: ["build"]`. Groups the elements describing this artifact result. |
| `result` and `manifest` | The logical result is a `software_Package`. It `contains` its primary deliverables and `hasMetadata` a `software_File` for the [output manifest](./artifact-model.md#the-output-manifest), with `software_primaryPurpose: "manifest"` and checksum `M`. |
| `build` and `agent` | `build_Build` with `build_buildType: "urn:spdx:gardenlinux.io/glbx/build-types/<kind>"` (the build type is shared across SBOM namespaces). The target Debian architecture is the `arch` build parameter. The build is `invokedBy` the glbx `SoftwareAgent`. |
| `creation` | A shared `CreationInfo` carries the SPDX version, creator and creation time; glbx uses the normalized epoch `1970-01-01T00:00:00Z` so generation is reproducible. |
| `outputs/<name>` | One element per named payload or control output. A Debian binary is a `software_Package` with its name, version, architecture-bearing PURL and archive checksum; a control stanza, rootfs archive or disk image is a `software_File` with its own checksum. The output name is preserved as an identifiable URI segment, e.g. `outputs/control%3Afixture` for `control:fixture`. |
| `source` and `payload` | `source` is the Debian source package for a package or tooling build. For a rootfs, `payload` is the logical filesystem the delivered rootfs archive represents. |
| `inputs/…` and `environment/<role>` | Elements for locally described materials and for the packages present in a build environment (`chroot` for a source build, `bootstrap` for binary validation, `layer1` for the discarded rootfs configuration tooling, `assembly` for an image build). Foreign glbx inputs instead use their producer's existing IDs. |
| `documents/<N>` | A `software_File` in the *consumer's* document describing the producer's serialized SPDX document for output-manifest hash `N`. |
| `relationships/<H>` | A relationship belongs to the document asserting it, even when its `from` is foreign. Its identity folds the source ID, relationship type, lifecycle scope, target ID and comment, as applicable. |

### Materials described in the consuming document

When a material's origin is local rather than another glbx build, the document
describes it directly:

| Material | What the element identifies |
| --- | --- |
| Upstream orig tarball | A `software_File` identified by the archive name and the checksum of the actual `.orig.tar.*` bytes; each supplied component orig is recorded separately. Among the Debian source materials, only a real orig archive is a checksummed tarball — and it is the one class of source input pinned by its own bytes (through `sources.yml`) rather than carried inside the package directory. |
| Debian packaging directory | A `software_Package` with source purpose and an `externalRef` of type `vcs`, whose Git source reference identifies the repository, commit and repo-relative subdirectory (e.g. `pkgs/openssl/src/debian`). No directory hashes, Git tree IDs or synthetic `.dsc` / `debian.tar.*` artifacts. A native source package kept in Git references its source subdirectory the same way. |
| Build recipe (`build.yml`), configuration, script, certificate or staged file | A `software_File` for the actual file glbx consumed. `build.yml` governs the source build — build profiles, options, `pkg_type`, signing — so it is recorded as an input and `configures` the build. The packaging directory and its patches are already covered by the Git reference. |
| Lockfile-provided package | Described locally. A binary tool carries its binary coordinates and archive checksum; a static-link declaration naming a source package is a source-purpose package with the declared source name and version. |
| Package supplied by an artifact `Depends` | Uses the producer's existing element — a chroot tool references the actual glbx binary output; a static-link declaration naming a Debian source references the producer's source-package element when identified — under the import rule below. |

These source inputs are recorded as *evidence*, and are not all addressed the
same way glbx's identity model addresses them. A package directory's bytes —
`build.yml`, the `src/debian` packaging tree, every script and certificate under
it — are folded as a whole into the source build's content identity through the
directory hash, so the document's job is to make the relevant ones individually
visible (the packaging tree by Git reference, `build.yml` and other consumed
files by their own checksum), not to re-pin bytes the identity already covers.
The orig tarball is the exception: it lives outside the package directory and is
pinned by its own hash in `sources.yml`, so its `software_File` checksum is the
primary record of those bytes.

The Git repository is selected from `GLBX_SBOM_SOURCE_REPO` when set, else the
current repository's `origin` URL, else the absolute filesystem path of the
repository root; the commit and subdirectory are always recorded alongside it.
Byte checksums use SPDX `verifiedUsing`. A Debian PURL carries package
coordinates (e.g. `pkg:deb/debian/autoconf@2.73-2?arch=all`) using the effective
Debian version, the package architecture (including `all`), `arch=source` for a
source package, and the `gardenlinux` vendor for Garden Linux builds.

### SPDX relationships

An edge reads from SPDX `from` to SPDX `to`. Build-scoped facts —
including `hasStaticLink` — are `LifecycleScopedRelationship` with
`scope: "build"`; unscoped facts such as byte copies and containment are plain
`Relationship`. Each assertion names a single target.

| Direction | What the edge establishes |
| --- | --- |
| Build → `hasInput` → source or artifact input | The source materials, each output supplied by an artifact `Depends` edge, and any other file actually consumed as a build input. |
| Build → `usesTool` → package | A direct edge to **each** package in the build chroot, transitive packages included, whether or not any command from it ran. |
| `build.yml` → `configures` → build | The build recipe governed the build; the build also has a `hasInput` edge to the `build.yml` it read. |
| Build → `hasOutput` → output | The build emitted that named payload or control output. |
| Product → `contains` → component | The component ships in the product; an installed Debian package represents shipped software at package granularity. Inclusion of the actual `.deb` archive is claimed only when the product carries the archive bytes. |
| Source-build output → `copiedTo` → validation output | The two stages own distinct output elements with identical archive or control bytes; the validation document asserts the copy. |
| Emitted binary → `hasStaticLink` → declared package | One edge per distinct package and version named by `Built-Using` or `Static-Built-Using` in that binary's final control stanza. |
| Rootfs archive → `expandsTo` → logical filesystem | Connects the tar archive to the filesystem whose contents it represents. |
| Product → `hasMetadata` → SPDX file | Connects a Debian package to its control stanza, or a logical result to its output manifest. |

Where the document groups packages by build environment (`chroot`, `bootstrap`,
`layer1` or `assembly`), that environment's `contains` inventory sits
alongside the direct `build → usesTool → package` edges, using the actual chroot
after package selection and local overrides. A glbx-built package installed
through an artifact `Depends` edge appears as both `hasInput` and `usesTool`,
pointing at the same producer output. A package supplied by `build-deps.yml` is a
local package entry for the lockfile selection. No invocation tracking is
involved.

## Cross-document inputs

When one build consumes another's outputs, the consuming document does not
re-describe them. The glbx source build emits `fixture.deb` and `control:fixture`;
binary validation consumes both and re-emits them. The validation document
records **which producer elements it used** and **which file bytes define those
elements**, in three parts.

### Naming the foreign SPDX elements

The consumer's `SpdxDocument` carries one SPDX `ExternalMap` per distinct foreign
element. Each `externalSpdxId` is the ID the producer minted; both
`definingArtifact` references point at the same local `software_File` that pins
the producer document. (This `ExternalMap` is an SPDX record, distinct from an
object-store map entry.)

```json
{
  "type": "SpdxDocument",
  "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/document",
  "import": [
    {
      "type": "ExternalMap",
      "externalSpdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/control%3Afixture",
      "definingArtifact": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/documents/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c",
      "locationHint": "b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c.spdx.json"
    },
    {
      "type": "ExternalMap",
      "externalSpdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb",
      "definingArtifact": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/documents/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c",
      "locationHint": "b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c.spdx.json"
    }
  ]
}
```

### Pinning the file that defines those elements

The second part is a `software_File` in the consumer's `@graph` whose `spdxId`
matches both `definingArtifact` values above. Its `verifiedUsing` checksum covers
the producer's *entire* serialized document, whose own `SpdxDocument` element
lives in that external file.

```json
{
  "type": "software_File",
  "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/documents/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c",
  "name": "b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c.spdx.json",
  "contentType": "application/spdx+json",
  "verifiedUsing": [
    {
      "type": "Hash",
      "algorithm": "sha256",
      "hashValue": "c82446d1daecce32fcfc11cc5d57752b49ab3f6ec5bb088070f4f244e64fef7b"
    }
  ],
  "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
}
```

### Using a foreign element as a build input

With the element named and its defining file pinned, the validation build's
`hasInput` edge points directly at the original package element in the producer's
graph.

```json
{
  "type": "LifecycleScopedRelationship",
  "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a4cb89f83994be65a8354ab0008778b88cb1d277e27ddfe1f02d9884686bcbe2",
  "relationshipType": "hasInput",
  "scope": "build",
  "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
  "to": [
    "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb"
  ],
  "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
}
```

To follow such a reference offline, a reader opens the sibling file named by
`locationHint`, checks its bytes against the `software_File` checksum, and finds
the exact `externalSpdxId` in the producer's graph. Here `a31495…` is the
validation output-manifest hash and `b8f87e…` is the source-build
output-manifest hash — which is why the exported producer file begins with
`b8f87e…`, its bytes hash to `c82446…`, and the package entry inside carries the
separate `fixture.deb` archive checksum `2df848…`.

The same import rule applies to every foreign reference, including a foreign
`from` on a `copiedTo` relationship. Every producer document in a closure uses the
root's SBOM namespace, and the closure is discovered by following
`SpdxDocument.import`. A Git URL, PURL or JSON-LD context is a different kind of
reference and not an import. SPDX relationships may form cycles, but the document
import graph is **acyclic**, because each consumer pins its producers' document
bytes.

## Mapping artifacts to SPDX

The graph and relationships above apply to every artifact kind; what differs is
which inputs, tools and outputs each kind contributes. The kinds correspond to the
build actions in [package building](./package-build.md) and
[image assembly](./image-assembly.md).

### Debian source build · `debian-pkg-build`

The build gets `hasInput` edges to the logical Debian source, each supplied orig
archive, the Git packaging reference, the `build.yml` recipe, and the inputs
supplied by artifact `Depends`; a direct `usesTool` edge to every package in the
build chroot; and a `hasOutput` edge for each emitted `<bin>.deb` and
`control:<bin>`. The logical result `contains` each primary package, and each
package `hasMetadata` its control stanza.

Every package in the build chroot comes from the source package's
`build-deps.yml` lockfile (minus any a locally-built dependency overrides), and
each one is recorded the same way: a `software_Package` under
`inputs/deb/<name>/<version>/<arch>/<sha256>`, carrying the tool's Debian PURL and
the `.deb`'s archive checksum, reached by a `build → usesTool → <pkg>` edge and a
`chroot environment → contains → <pkg>` edge. This covers the whole chroot
closure, transitively — not only the tools a command happened to invoke — so a
compiler toolchain shows up as its constituent packages. For example, `gcc` from
`build-deps.yml` becomes

```json
{
  "type": "software_Package",
  "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/gcc/14.2.0-19/amd64/29d6be30670a62dca93afd942d82479d0a10ed50d8b8e7736231cdbcd1c5b5dc",
  "name": "gcc",
  "verifiedUsing": [{ "type": "Hash", "algorithm": "sha256", "hashValue": "29d6be30670a62dca93afd942d82479d0a10ed50d8b8e7736231cdbcd1c5b5dc" }],
  "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
  "software_packageUrl": "pkg:deb/debian/gcc@14.2.0-19?arch=amd64",
  "software_packageVersion": "14.2.0-19"
}
```

with a `build → usesTool → gcc` relationship and a `chroot → contains → gcc`
relationship pointing at that element. The [worked
example](#source-build-spdx-document) carries full `binutils`, `gcc` and `make`
entries with these edges alongside the one-tool `dpkg` from the original fixture.
A tool that is itself locally built (an entry satisfied by an artifact `Depends`
rather than the lockfile) is not minted here — its `usesTool` edge points at the
producing build's own `.deb` output, as the materials table above describes.

For a quilt package the logical Debian source `contains` the orig archive(s) and
the packaging Git reference. In glbf-staging, OpenSSL's orig archive supplies the
upstream bytes and `pkgs/openssl/src/debian` holds the packaging and patches, with
the Git commit and subdirectory identifying that packaging input.

#### From a control stanza to static-link edges

These edges are the SBOM's record of a critical fact that nothing else in the
graph captures: a binary that statically embeds code from another source package
inherits that source's vulnerabilities, with no runtime dependency edge to betray
the link. Go binaries are the standing case — a CVE in a package named only by
`Static-Built-Using` can affect the shipped binary even though that package
appears nowhere in its runtime closure. So glbx records the static link
explicitly rather than leaving it to be inferred.

For each emitted `.deb`, glbx reads **its own final control stanza** and takes the
union of the entries in `Built-Using` and `Static-Built-Using`. Every distinct
declared package and version yields one build-scoped
`<that .deb> → hasStaticLink → <declared package>` edge; either field alone
suffices, and when both are absent or empty the output gets no such edges. This is
glbx's interpretation of the two Debian fields — it needs no linker trace, ELF
inspection or per-executable evidence. For example, a stanza

```text
Package: example-cli
Built-Using: go-helper (= 1.2)
Static-Built-Using: go-helper (= 1.2), bundled-lib (= 3.0)
```

gives `example-cli.deb` two `hasStaticLink` edges, to `go-helper@1.2` and
`bundled-lib@3.0`; a sibling binary from the same build gets only the edges its
own stanza warrants. A lockfile-supplied source is a local source-package entry; a
source supplied by another glbx build references that producer's source element;
when only the declaration is known, its source name and version are recorded
locally without guessing a producer.

Debian `Build-Depends` (and its variants), `Depends` and `Pre-Depends` generate
**no** SPDX edges. Build inputs come from the artifact `Depends` relation and
tools from actual chroot membership; a Debian dependency clause contributes
neither an inferred runtime `dependsOn` nor an extra build prerequisite.

### Binary package validation · `debian-binary-pkg`

The validation build has `hasInput` edges to the original source-build package
and its control stanza, plus the artifact inputs used for installability
validation, describing the actual packages selected for the check (permitted
lockfile inputs included). Every package in the validation chroot gets a direct
`usesTool` edge, including the essential bootstrap packages drawn from the source
build's lockfile.

New output IDs are minted under the validation output-manifest hash for the
re-emitted `.deb` and control stanza. The document asserts `hasOutput` for both,
plus two `copiedTo` edges from the source-build outputs to their byte-identical
validation outputs; the original binary's control-derived `hasStaticLink` edges
remain reachable through `copiedTo`. The installability check adds no runtime
dependency edges. The output actually supplied is the one referenced — a
co-produced sibling can be taken directly from a completed source build, as with
`libssl3t64` and `openssl-provider-legacy` in staging — and importing the
source-build outputs keeps the validation documents independent of one another.

### Image tooling packages · `tooling-pkg-build`

The source-build rules apply to `images.d/tooling/<src>`, using that tooling
build's own inputs and build lock. The tooling build emits its own binary packages
and controls, and an image-assembly document can import a tool directly from the
tooling build's `outputs/<bin>.deb` element.

### Root filesystem · `rootfs`

A rootfs is assembled through a three-layer overlay, and its two package
populations are described separately because only one of them ships. **Layer 0**
(the locally-built packages) and **Layer 2** (the mutations `dpkg` writes while
configuring) together are the shipped filesystem; **Layer 1** is the lockfile
Debian infrastructure — raw-extracted only so `dpkg` can configure against it, and
then left out of the final image. The build consumes the selected package set from
`rootfs-<label>.yml`, the effective configuration scripts and relevant control
stanzas, and the output is `rootfs.tar.gz`.

The `layer1` tool environment describes the packages present only to configure the
rootfs — the discarded Layer 1, including selections from `rootfs-deps.yml` — and
the build `usesTool` it. The shipped packages are not an environment: the document
records `rootfs.tar.gz → expandsTo → payload`, and from the logical filesystem
`payload` uses `contains` to reach the validated package elements actually present
in the final filesystem (Layer 0 plus Layer 2), accounting for additions and
removals by the scripts. So a local `dpkg` that the Garden Linux base rootfs ships
appears under `payload`'s `contains`, while a mirror `dpkg` extracted only into
Layer 1 to drive configuration appears in the `layer1` environment and never in
the shipped set.

### Assembled image · `image`

The build gets `hasInput` edges to the rootfs archive, the image recipe and its
literal inputs, and each image output supplied to the recipe; its `assembly` tool
environment describes the effective mirror tools and locally built helpers
populated into the assembly container; its `hasOutput` is the output named
`image`. Product `contains` relationships are used only for content the recipe
carries into the delivered image.

- **Disk image.** When the disk carries the base filesystem across its partitions,
  its image file `contains` the rootfs producer's logical `payload`, and also
  `contains` the exact initrd image output copied onto the ESP. The build
  `usesTool` the locally built `mkfat32`, patched `e2fsprogs` and `mv-data`
  packages, whose own SPDX imports reveal how they were built.
- **Pruned initrd.** The cpio recipe consumes a rootfs, removes files and the dpkg
  database, and prunes modules; the rootfs is recorded as a build input and
  `contains` is asserted only for components evidenced in the final initrd. The
  same principle holds for any recipe that discards or transforms its inputs.

## Object store storage and lookup

A serialized SPDX document is an ordinary [object-store](./object-store.md) blob,
and a **map entry** from the SBOM map key `K(A,M)` to the document checksum `D`
records where it lives. That key is folded, by the usual glbx hash-fold
convention, from the dedicated SBOM seed, the SBOM namespace and the
output-manifest hash. The name `<M>.spdx.json` belongs to the exported file;
local glbx consumers use the object-store interface, which treats the SPDX bytes
as an opaque blob like any other.

Because the key folds in the namespace, one output manifest reproduced both
locally and in CI carries the same `M` but two independent SBOM map entries: the
entry for `(localhost,M)` can already exist when a developer asks for
`(gardenlinux.io,M)`, the configured pull-through store resolves the CI entry
independently, and both documents coexist locally.

The first valid SBOM map entry for a given namespace and output manifest
establishes the canonical SPDX bytes. A reproducible rebuild — including a
candidate differing only in its creation timestamp — reuses those bytes, and
concurrent writers converge on the established document, which consumers pin by
checksum. Inconsistent material evidence or damaged content is reported, and the
established entry preserved; retaining an SBOM also retains its imported documents,
across local cleanup and later pull-through reads.

The dedicated seed scopes only the SBOM map key. Rotating it gives the same
namespace and output manifest a fresh key that, for an established result, still
designates the same SPDX bytes, leaving existing producer-checksum pins valid. The
public SPDX IDs and the OCI tag `sbom-<M>` do not include the seed.

## OCI layout and publication

glbx publishes the same SPDX bytes in two forms for two readers. A glbx consumer
uses the **ordinary object-store representation in OCI** to fetch one document; an
ORAS consumer uses an **OCI closure manifest** to fetch the whole SPDX closure in
one pull. Both forms reference the same OCI blobs. The underlying OCI conventions
are those of the [remote cache](./remote-cache.md).

### Objects in the target OCI repository

For a requested output-manifest hash `M`, let `N` range over `M` and every
producer hash reached by SPDX imports, and let `D_N` be the document checksum for
`N` in the selected SBOM namespace `A`.

| OCI object or tag | What the registry stores or resolves |
| --- | --- |
| **Blob** at `sha256:<D_N>` | The raw, uncompressed SPDX document bytes for `N`. The local counterpart is the object-store blob with content hash `D_N`. |
| **Tag** `blob-<D_N>` | The ordinary glbx **single-layer wrapper manifest** whose layer descriptor references the SPDX content blob — the same wrapper format as for any object-store blob. |
| **Tag** `map-<K(A,N)>` | The **same** single-layer wrapper manifest as `blob-<D_N>` (the two tags resolve to one manifest digest). This is the OCI form of the named SBOM map entry. |
| **Tag** `sbom-<M>` | A separate **closure manifest** for the requested result, whose layer descriptors reference the SPDX content blobs for the entire closure directly. One such tag per requested `M`, including a result with no imports. |

The closure manifest is an OCI image manifest (`schemaVersion: 2`, media type
`application/vnd.oci.image.manifest.v1+json`) with glbx artifact type
`application/vnd.gardenlinux.glbx.sbom-closure` and the standard empty config
(`application/vnd.oci.empty.v1+json`, content `{}`). The root document is first,
followed by one layer per distinct imported document in output-manifest-hash
order; each layer descriptor has media type `application/spdx+json`, the digest
and size of the exact bytes, and `org.opencontainers.image.title: "<N>.spdx.json"`.
The flat filenames let SPDX `locationHint` references resolve in the downloaded
directory.

The closure manifest adds only its own manifest bytes per result; its layer
descriptors reuse the blobs the single-layer wrappers already reference. An
optional release alias is another tag onto the same closure manifest, and release
aliases never enter SPDX IDs or content.

### What an ORAS user sees

Given the output-manifest hash `M`, the following pulls the root and every
imported file; `oras resolve` on the same tag would return the closure manifest
digest.

```sh
repo=ghcr.io/gardenlinux/glbf-staging/objstore
oras pull -o sboms "${repo}:sbom-${M}"
jq '.["@graph"][] | select(.type == "SpdxDocument") | {rootElement, import: .["import"]}' \
  "sboms/${M}.spdx.json"
```

For the [worked example](#worked-example--debian-source-build-and-binary-validation),
the first layer descriptor names the validation file and the second the
source-build file; the filename stems are output-manifest hashes and the layer
digests are the document checksums.

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "config": {
    "mediaType": "application/vnd.oci.empty.v1+json",
    "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
    "size": 2
  },
  "artifactType": "application/vnd.gardenlinux.glbx.sbom-closure",
  "layers": [
    {
      "mediaType": "application/spdx+json",
      "digest": "sha256:ae15bb5204d95dc8cce4523d0153c73be34e835927fdfa1260d252279f4bb4fa",
      "size": 25455,
      "annotations": {
        "org.opencontainers.image.title": "a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e.spdx.json"
      }
    },
    {
      "mediaType": "application/spdx+json",
      "digest": "sha256:c82446d1daecce32fcfc11cc5d57752b49ab3f6ec5bb088070f4f244e64fef7b",
      "size": 27048,
      "annotations": {
        "org.opencontainers.image.title": "b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c.spdx.json"
      }
    }
  ]
}
```

### Publish modes

Publishing artifact results includes SBOM publication by default; ordinary
artifact publication still handles payloads, output manifests and graph aliases as
[described for distributed builds](./distributed-builds.md). SBOM publication is
also detachable: it takes a set of output-manifest hashes and an SBOM namespace,
and can run on another machine that reaches the configured pull-through store.

| Entry point | Which closures are published |
| --- | --- |
| Publish a graph | One closure per selected output-manifest hash. Ordinary input blobs, payloads and logs do not each become SPDX roots. |
| Publish a target | The closure rooted at the selected target's output-manifest hash. |
| Publish `--manifest M` | The closure for precisely that output-manifest hash, including a retained divergent rebuild; the existing graph-identity-alias behaviour is preserved. |
| Deferred `--aliases-to` / `--aliases-from` | Before the build runner disappears, the SPDX documents are made discoverable through their SBOM map entries in OCI. The later publisher recovers the intended output-manifest hashes and namespace without a checkout, publishes the closures, and respects the existing reproducibility gate on graph aliases. Log aliases never select an SPDX root; ambiguous roots or conflicting namespace selections are reported. |

For each requested result, glbx follows every SPDX import through the pull-through
store — including producer documents held only in the remote OCI repository —
verifying each producer's pinned checksum and referenced element, and ensures the
blobs and ordinary wrapper tags exist for every member before exposing the closure
tag. A missing document, a conflicting producer checksum or an unresolved
reference makes a closure incomplete; repeating a successful publication preserves
the existing closure.

The public tag `sbom-<M>` has no namespace component: Garden Linux CI owns it in
its publication repository and uses the `gardenlinux.io` namespace, while normal
local builds and object-store reads use their object-store keys. Only explicit
publication creates the closure tag. If another namespace claims an existing
closure tag, or a parent pins different bytes than those established at the
destination, the conflict is reported and the existing tag and object-store
bindings preserved.

## Why byte-identical outputs still get distinct SBOMs

Keying one SPDX document per *output-manifest hash* only works because an output
manifest already distinguishes builds that happen to emit the same bytes. A
source build can emit exactly `fixture.deb` and `control:fixture`, and binary
validation can re-emit those two files byte for byte; if a manifest named only its
payload and control outputs, the two builds would share one output-manifest hash
and so compete for a single SBOM address, even though one compiled the package and
the other merely checked it. Changing a build input while producing identical
payloads would create the same collision.

glbx avoids this at the manifest layer: every output manifest also names a
reserved `.glbx-provenance` output whose deterministic bytes fold in the build's
full target key and graph identity, so distinct build histories produce distinct
output-manifest hashes even when every payload is identical. The mechanism, and
its bounds, are part of the output manifest itself —
[the artifact model](./artifact-model.md#the-output-manifest) introduces it and
[the engine implementation](../impl/engine.md#the-provenance-output-and-manifest-uniqueness)
gives the exact bytes. From the SBOM's side the consequences are simply that:

- two builds that differ in target, graph identity or output bytes get different
  output-manifest hashes, hence different SPDX IDs, OCI tags and SBOM addresses;
- a reproducible rebuild with the same target, identity and outputs reuses the
  established document, since the SBOM namespace and build-run timestamp are
  independent of the output manifest;
- the `.glbx-provenance` entry is carried by the generic object-store operations
  but excluded from the payloads of Debian packages, root filesystems and images,
  so a downloaded SPDX closure contains only SPDX documents.

Where the graph identity cannot distinguish two builds that still emit identical
files — the bound stated for the manifest — glbx reports the provenance
discrepancy rather than overwriting an established document. A future incompatible
SBOM change that needs a different public identity for an already-established
result gives that result a new output-manifest hash.

## Worked example · Debian source build and binary validation

A Debian source package named `fixture` produces one architecture-independent
`fixture.deb` and its `control:fixture` stanza on an `amd64` target. Its upstream
input is `fixture_1.orig.tar.xz` and its Debian packaging input is a Git reference
to `pkgs/fixture/src/debian`. A second target checks installability and re-emits
both outputs. Both documents use namespace `gardenlinux.io` and one example chroot
tool, `dpkg`. The package data and repository are synthetic; neither static-link
control field appears here (the [control-stanza example](#from-a-control-stanza-to-static-link-edges)
above shows the case where both are present).

### Follow the package

Below, `S/` is the source build's element prefix and `V/` the validation build's;
their output-manifest hashes begin `b8f87e…` and `a31495…`. The two inline
documents use the full IDs.

| In this SBOM | Follow these entries | What the reader learns |
| --- | --- | --- |
| Source build | `S/build → hasInput → S/source`, plus direct inputs for the orig archive and the packaging Git reference. | The `software_File` pins the orig archive bytes; the source-purpose `software_Package` identifies the packaging by Git repository, commit and subdirectory. |
| Source build | `S/build → usesTool → S/inputs/deb/dpkg/…` (and the same for `binutils`, `gcc`, `make`, …); the `chroot` environment also `contains` each. | Every `build-deps.yml` package in the chroot — `dpkg`, `binutils`, `gcc`, `make` and the rest of the closure — gets a direct tool edge from the build and a membership edge from the environment. |
| Source build | `S/build → hasOutput → S/outputs/fixture.deb`; the package `hasMetadata` `S/outputs/control%3Afixture`. | The delivered archive and its separate control stanza are identifiable, each with its own checksum. |
| Binary validation | `V/build → hasInput → S/outputs/fixture.deb`; a second input selects `S/outputs/control%3Afixture`. | Both inputs use the producer's original IDs; the two `ExternalMap` entries name the same local `software_File`, pinning the producer SBOM once. |
| Binary validation | `V/build → usesTool → V/inputs/deb/dpkg/…`; the bootstrap environment also `contains` it. | The validation build records its own chroot tool even when it selected the same lockfile package. |
| Binary validation | `S/outputs/fixture.deb → copiedTo → V/outputs/fixture.deb` and `V/build → hasOutput → V/outputs/fixture.deb`; likewise for the control stanza. | The source and validation occurrences have different IDs; matching checksums establish which bytes glbx re-emitted, and both copy assertions live in the validation document. |

### Identify the bytes behind each checksum

| SHA-256 in the example | Bytes identified | Where it appears |
| --- | --- | --- |
| `b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c` | Output manifest for the source build | Source-build ID prefix and exported source-build filename. |
| `a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e` | Output manifest for binary validation | Validation ID prefix, exported validation filename and public OCI tag suffix. |
| `c82446d1daecce32fcfc11cc5d57752b49ab3f6ec5bb088070f4f244e64fef7b` | Entire serialized source-build document | Consumer's `software_File.verifiedUsing` checksum, object-store blob hash, and source SPDX content OCI blob digest. |
| `ae15bb5204d95dc8cce4523d0153c73be34e835927fdfa1260d252279f4bb4fa` | Entire serialized validation document | Object-store blob hash and root SPDX content OCI blob digest in the closure manifest. |
| `2df8489d2a625c46969f0199ed9385b48c556268ab269b8aa871132607f6f45a` | Complete `fixture.deb` archive | Both output manifests and both SPDX package elements. |
| `8e9e15289a973e1215bd5cec4f8133558df06ab10a06e9439311f8794b2141c9` | Complete Debian control stanza | Both output manifests and both SPDX control-file elements. |

The listings below contain every element for this one-tool example and are
indented for reading. Their SPDX checksums and OCI layer sizes refer to the JSON
serialized with RFC 8785 (the JSON Canonicalization Scheme), not to the Markdown
display.

### Source-build SPDX document

The exported file is `b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c.spdx.json`. From the `result` element, `contains` reaches the package and `hasMetadata` reaches the `software_File` describing the output manifest.

```json
{
  "@context": "https://spdx.org/rdf/3.0.1/spdx-context.jsonld",
  "@graph": [
    {
      "type": "SoftwareAgent",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/agent",
      "name": "glbx",
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "build_Build",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "build_buildType": "urn:spdx:gardenlinux.io/glbx/build-types/debian-pkg-build",
      "build_parameter": [
        {
          "type": "DictionaryEntry",
          "key": "arch",
          "value": "amd64"
        }
      ]
    },
    {
      "type": "CreationInfo",
      "@id": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "created": "1970-01-01T00:00:00Z",
      "createdBy": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/agent"
      ],
      "specVersion": "3.0.1"
    },
    {
      "type": "SpdxDocument",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/document",
      "rootElement": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/sbom"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "element": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/agent",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/environment/chroot",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/binutils/2.44-5/amd64/6016f2be858595f08b377c4c03b3b30d7948352596f51cdd17b62709c2e975ad",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/dpkg/1/amd64/f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/gcc/14.2.0-19/amd64/29d6be30670a62dca93afd942d82479d0a10ed50d8b8e7736231cdbcd1c5b5dc",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/make/4.4.1-2/amd64/f27d865313cc2063d3415dc0641501e7b6d73d58b04e9f308a92d3dbcc30d4cd",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/file/fixture_1.orig.tar.xz/feccc439aca5debd0208adb22aa1df1ab3c43de25dac31033b9f3625f815a68e",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/file/pkgs%2Ffixture%2Fbuild.yml/e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/git/packaging",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/manifest",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/control%3Afixture",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/07de1893f3334c5fd7a23e4bd7b9bc9973519f56ab46cea33d9093f39a49af04",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/08c324c45a78dd7a98c3a72381ec064c30f5ab55f662b27b0681eb942c1c3bd8",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/10d5aaa8c5bcccca890f3a4f2380811d18cb95a692e75852efc277647e18038d",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/169af0c3af4d76fb0b57c36b31ee18d5da8fb9e1e2a08250349cc7c2691f5f6f",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/16e04448f3e10154d016dce981387a65c652d0351ad24c9f3d81d117b90f507b",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/1e42e0f3906801934618a0986df2dec99737570467229fd9306f6f63da08ee3f",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/1fa68fe7909b474a0cc42409e280ce3d9ebad1b6fa912c8c15da2c1d2fa859b4",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/20f5184815e49783064da63e3c6b2c71506884fed54a9cfe54d89a2bccacfa6f",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/23b2424fadf10655d53e293e0d7c5b5697a20481bc6eab6d34002d846c56bd20",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/478d10f4576cf5c6a23a80da6b7dd1b0ed3e7d027a9a21807b22d7929a04140c",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/55ea603de765274ca99e59b83ae3c63244c47e9b1ea6f96f72e05c1db9dbd40e",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/5cc36183d7ffd822210bd7631908efdf4df140c6e60282b5d8aef416be40a95a",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/6e558f587229f4960933c370049fab920af3bc1c3538382e64145eabbf694f51",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/75cf8144047332bdef0cb36d781f2696d49e1eb784cda013731109fc424615aa",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/77b5c7e7c322f0330fa0d7571f82c02e8d767a1a43c846bfad486223d31864e6",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/86c6a730fce29e278ee2fe1a476a50bde5753074f3cec9719d05c7886f8c048e",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/9eb5ae0d7ca8776690ef6a5d4d96f8f8f5c5cd6831ba4c79095811bdce635208",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/b62b51f4d511d9bb7b33fd3c93669c0d6a0822aa425c463e93bb687241014e69",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/c7eb4633b2405162549eb2b808b7c28be1b2a3c884792aef7fb2f703b0c974b0",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/ccc53a504fe3ae1e10dc3c5d4a6777e4f7cd0f4ee57c92b29c4ca48be86c7bad",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/e93677db8e0e8749be983ebfb06f9b9ded127561960e09b2648b6e5f2e0be9f5",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/efbd6ba658243e37cab7e5f44efd8e1210112071d16e203e58a2c185a6df75a2",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/result",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/sbom",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/source"
      ],
      "profileConformance": [
        "build",
        "core",
        "software"
      ]
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/environment/chroot",
      "name": "build chroot",
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/dpkg/1/amd64/f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56",
      "name": "dpkg",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "software_packageUrl": "pkg:deb/debian/dpkg@1?arch=amd64",
      "software_packageVersion": "1"
    },
    {
      "type": "software_File",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/file/fixture_1.orig.tar.xz/feccc439aca5debd0208adb22aa1df1ab3c43de25dac31033b9f3625f815a68e",
      "name": "fixture_1.orig.tar.xz",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "feccc439aca5debd0208adb22aa1df1ab3c43de25dac31033b9f3625f815a68e"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "software_File",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/file/pkgs%2Ffixture%2Fbuild.yml/e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b",
      "name": "pkgs/fixture/build.yml",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/git/packaging",
      "name": "pkgs/fixture/src/debian",
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "software_primaryPurpose": "source",
      "externalRef": [
        {
          "type": "ExternalRef",
          "externalRefType": "vcs",
          "locator": [
            "git+https://git.example.org/gardenlinux/recipes.git@25ee771699c83c55d98fab8726b039d6db98131c#pkgs/fixture/src/debian"
          ]
        }
      ]
    },
    {
      "type": "software_File",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/manifest",
      "name": "glbx output manifest",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "software_primaryPurpose": "manifest"
    },
    {
      "type": "software_File",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/control%3Afixture",
      "name": "control:fixture",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "8e9e15289a973e1215bd5cec4f8133558df06ab10a06e9439311f8794b2141c9"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb",
      "name": "fixture.deb",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "2df8489d2a625c46969f0199ed9385b48c556268ab269b8aa871132607f6f45a"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "software_packageUrl": "pkg:deb/gardenlinux/fixture@1?arch=all",
      "software_packageVersion": "1"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/07de1893f3334c5fd7a23e4bd7b9bc9973519f56ab46cea33d9093f39a49af04",
      "relationshipType": "contains",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/result",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/08c324c45a78dd7a98c3a72381ec064c30f5ab55f662b27b0681eb942c1c3bd8",
      "relationshipType": "contains",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/source",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/git/packaging"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/10d5aaa8c5bcccca890f3a4f2380811d18cb95a692e75852efc277647e18038d",
      "relationshipType": "hasInput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/file/pkgs%2Ffixture%2Fbuild.yml/e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/169af0c3af4d76fb0b57c36b31ee18d5da8fb9e1e2a08250349cc7c2691f5f6f",
      "relationshipType": "hasMetadata",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/control%3Afixture"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/16e04448f3e10154d016dce981387a65c652d0351ad24c9f3d81d117b90f507b",
      "relationshipType": "configures",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/file/pkgs%2Ffixture%2Fbuild.yml/e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/1e42e0f3906801934618a0986df2dec99737570467229fd9306f6f63da08ee3f",
      "relationshipType": "hasOutput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/control%3Afixture"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/1fa68fe7909b474a0cc42409e280ce3d9ebad1b6fa912c8c15da2c1d2fa859b4",
      "relationshipType": "hasInput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/git/packaging"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/20f5184815e49783064da63e3c6b2c71506884fed54a9cfe54d89a2bccacfa6f",
      "relationshipType": "contains",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/source",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/file/fixture_1.orig.tar.xz/feccc439aca5debd0208adb22aa1df1ab3c43de25dac31033b9f3625f815a68e"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/23b2424fadf10655d53e293e0d7c5b5697a20481bc6eab6d34002d846c56bd20",
      "relationshipType": "usesTool",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/environment/chroot"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/478d10f4576cf5c6a23a80da6b7dd1b0ed3e7d027a9a21807b22d7929a04140c",
      "relationshipType": "usesTool",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/dpkg/1/amd64/f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/55ea603de765274ca99e59b83ae3c63244c47e9b1ea6f96f72e05c1db9dbd40e",
      "relationshipType": "contains",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/environment/chroot",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/dpkg/1/amd64/f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/6e558f587229f4960933c370049fab920af3bc1c3538382e64145eabbf694f51",
      "relationshipType": "hasInput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/source"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/75cf8144047332bdef0cb36d781f2696d49e1eb784cda013731109fc424615aa",
      "relationshipType": "invokedBy",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/agent"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/9eb5ae0d7ca8776690ef6a5d4d96f8f8f5c5cd6831ba4c79095811bdce635208",
      "relationshipType": "hasOutput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/c7eb4633b2405162549eb2b808b7c28be1b2a3c884792aef7fb2f703b0c974b0",
      "relationshipType": "hasMetadata",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/result",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/manifest"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/e93677db8e0e8749be983ebfb06f9b9ded127561960e09b2648b6e5f2e0be9f5",
      "relationshipType": "hasInput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/file/fixture_1.orig.tar.xz/feccc439aca5debd0208adb22aa1df1ab3c43de25dac31033b9f3625f815a68e"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/result",
      "name": "debian-pkg-build:fixture:amd64",
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "software_Sbom",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/sbom",
      "rootElement": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/result"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "element": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/agent",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/environment/chroot",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/binutils/2.44-5/amd64/6016f2be858595f08b377c4c03b3b30d7948352596f51cdd17b62709c2e975ad",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/dpkg/1/amd64/f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/gcc/14.2.0-19/amd64/29d6be30670a62dca93afd942d82479d0a10ed50d8b8e7736231cdbcd1c5b5dc",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/make/4.4.1-2/amd64/f27d865313cc2063d3415dc0641501e7b6d73d58b04e9f308a92d3dbcc30d4cd",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/file/fixture_1.orig.tar.xz/feccc439aca5debd0208adb22aa1df1ab3c43de25dac31033b9f3625f815a68e",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/file/pkgs%2Ffixture%2Fbuild.yml/e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/git/packaging",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/manifest",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/control%3Afixture",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/07de1893f3334c5fd7a23e4bd7b9bc9973519f56ab46cea33d9093f39a49af04",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/08c324c45a78dd7a98c3a72381ec064c30f5ab55f662b27b0681eb942c1c3bd8",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/10d5aaa8c5bcccca890f3a4f2380811d18cb95a692e75852efc277647e18038d",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/169af0c3af4d76fb0b57c36b31ee18d5da8fb9e1e2a08250349cc7c2691f5f6f",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/16e04448f3e10154d016dce981387a65c652d0351ad24c9f3d81d117b90f507b",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/1e42e0f3906801934618a0986df2dec99737570467229fd9306f6f63da08ee3f",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/1fa68fe7909b474a0cc42409e280ce3d9ebad1b6fa912c8c15da2c1d2fa859b4",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/20f5184815e49783064da63e3c6b2c71506884fed54a9cfe54d89a2bccacfa6f",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/23b2424fadf10655d53e293e0d7c5b5697a20481bc6eab6d34002d846c56bd20",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/478d10f4576cf5c6a23a80da6b7dd1b0ed3e7d027a9a21807b22d7929a04140c",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/55ea603de765274ca99e59b83ae3c63244c47e9b1ea6f96f72e05c1db9dbd40e",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/5cc36183d7ffd822210bd7631908efdf4df140c6e60282b5d8aef416be40a95a",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/6e558f587229f4960933c370049fab920af3bc1c3538382e64145eabbf694f51",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/75cf8144047332bdef0cb36d781f2696d49e1eb784cda013731109fc424615aa",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/77b5c7e7c322f0330fa0d7571f82c02e8d767a1a43c846bfad486223d31864e6",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/86c6a730fce29e278ee2fe1a476a50bde5753074f3cec9719d05c7886f8c048e",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/9eb5ae0d7ca8776690ef6a5d4d96f8f8f5c5cd6831ba4c79095811bdce635208",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/b62b51f4d511d9bb7b33fd3c93669c0d6a0822aa425c463e93bb687241014e69",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/c7eb4633b2405162549eb2b808b7c28be1b2a3c884792aef7fb2f703b0c974b0",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/ccc53a504fe3ae1e10dc3c5d4a6777e4f7cd0f4ee57c92b29c4ca48be86c7bad",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/e93677db8e0e8749be983ebfb06f9b9ded127561960e09b2648b6e5f2e0be9f5",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/efbd6ba658243e37cab7e5f44efd8e1210112071d16e203e58a2c185a6df75a2",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/result",
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/source"
      ],
      "software_sbomType": [
        "build"
      ]
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/source",
      "name": "fixture",
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "software_packageUrl": "pkg:deb/gardenlinux/fixture@1?arch=source",
      "software_packageVersion": "1",
      "software_primaryPurpose": "source"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/binutils/2.44-5/amd64/6016f2be858595f08b377c4c03b3b30d7948352596f51cdd17b62709c2e975ad",
      "name": "binutils",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "6016f2be858595f08b377c4c03b3b30d7948352596f51cdd17b62709c2e975ad"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "software_packageUrl": "pkg:deb/debian/binutils@2.44-5?arch=amd64",
      "software_packageVersion": "2.44-5"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/gcc/14.2.0-19/amd64/29d6be30670a62dca93afd942d82479d0a10ed50d8b8e7736231cdbcd1c5b5dc",
      "name": "gcc",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "29d6be30670a62dca93afd942d82479d0a10ed50d8b8e7736231cdbcd1c5b5dc"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "software_packageUrl": "pkg:deb/debian/gcc@14.2.0-19?arch=amd64",
      "software_packageVersion": "14.2.0-19"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/make/4.4.1-2/amd64/f27d865313cc2063d3415dc0641501e7b6d73d58b04e9f308a92d3dbcc30d4cd",
      "name": "make",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "f27d865313cc2063d3415dc0641501e7b6d73d58b04e9f308a92d3dbcc30d4cd"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation",
      "software_packageUrl": "pkg:deb/debian/make@4.4.1-2?arch=amd64",
      "software_packageVersion": "4.4.1-2"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/b62b51f4d511d9bb7b33fd3c93669c0d6a0822aa425c463e93bb687241014e69",
      "relationshipType": "usesTool",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/binutils/2.44-5/amd64/6016f2be858595f08b377c4c03b3b30d7948352596f51cdd17b62709c2e975ad"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/86c6a730fce29e278ee2fe1a476a50bde5753074f3cec9719d05c7886f8c048e",
      "relationshipType": "contains",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/environment/chroot",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/binutils/2.44-5/amd64/6016f2be858595f08b377c4c03b3b30d7948352596f51cdd17b62709c2e975ad"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/77b5c7e7c322f0330fa0d7571f82c02e8d767a1a43c846bfad486223d31864e6",
      "relationshipType": "usesTool",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/gcc/14.2.0-19/amd64/29d6be30670a62dca93afd942d82479d0a10ed50d8b8e7736231cdbcd1c5b5dc"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/5cc36183d7ffd822210bd7631908efdf4df140c6e60282b5d8aef416be40a95a",
      "relationshipType": "contains",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/environment/chroot",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/gcc/14.2.0-19/amd64/29d6be30670a62dca93afd942d82479d0a10ed50d8b8e7736231cdbcd1c5b5dc"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/ccc53a504fe3ae1e10dc3c5d4a6777e4f7cd0f4ee57c92b29c4ca48be86c7bad",
      "relationshipType": "usesTool",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/make/4.4.1-2/amd64/f27d865313cc2063d3415dc0641501e7b6d73d58b04e9f308a92d3dbcc30d4cd"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/relationships/efbd6ba658243e37cab7e5f44efd8e1210112071d16e203e58a2c185a6df75a2",
      "relationshipType": "contains",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/environment/chroot",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/inputs/deb/make/4.4.1-2/amd64/f27d865313cc2063d3415dc0641501e7b6d73d58b04e9f308a92d3dbcc30d4cd"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/creation"
    }
  ]
}
```

### Binary-validation SPDX document

The exported file is `a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e.spdx.json`. Both `ExternalMap` records in `SpdxDocument.import` point at one consumer-side `software_File` that pins the entire source-build document; the `externalSpdxId` values name the original package and control stanza within it.

```json
{
  "@context": "https://spdx.org/rdf/3.0.1/spdx-context.jsonld",
  "@graph": [
    {
      "type": "SoftwareAgent",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/agent",
      "name": "glbx",
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "build_Build",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation",
      "build_buildType": "urn:spdx:gardenlinux.io/glbx/build-types/debian-binary-pkg",
      "build_parameter": [
        {
          "type": "DictionaryEntry",
          "key": "arch",
          "value": "amd64"
        }
      ]
    },
    {
      "type": "CreationInfo",
      "@id": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation",
      "created": "1970-01-01T00:00:00Z",
      "createdBy": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/agent"
      ],
      "specVersion": "3.0.1"
    },
    {
      "type": "SpdxDocument",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/document",
      "rootElement": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/sbom"
      ],
      "import": [
        {
          "type": "ExternalMap",
          "externalSpdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/control%3Afixture",
          "definingArtifact": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/documents/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c",
          "locationHint": "b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c.spdx.json"
        },
        {
          "type": "ExternalMap",
          "externalSpdxId": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb",
          "definingArtifact": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/documents/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c",
          "locationHint": "b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c.spdx.json"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation",
      "element": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/agent",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/documents/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/environment/bootstrap",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/inputs/deb/dpkg/1/amd64/f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/inputs/file/pkgs%2Ffixture%2Fbuild.yml/e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/manifest",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/control%3Afixture",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/fixture.deb",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/01199ab04800adb706027aaa29e06ca53d313580e9c159da15038e60cec35dc9",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/1f012fac8b6961b3a90cdbaa0637627fa96e05cd51f4b40b2c822f685fe95803",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/2496ff20fa24d300f629873412ace80a4220fd0402c2f695078e2a31ae9fce03",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/32f14498ec8fb00277a3c84d568b9daa0d98dce201baff2cbacfd19aad6a5709",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/43edee2788cc7c89226c37c181d14bf452eb9b0ffc3da7ce261b4b60c245f78e",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/48148bcf7ba774cf728f5923d3f73a8cf18708c8035cae5bbc36ea5a81e87e21",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/4c6ef3711148e4c8b72031230cfb918a441f4f8d13719c34bed8f81c2c40654b",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/6cb90885fc0c1395983ad4e2b69f38af550b934cb3cb04d443b4b9ae7cef619b",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/8b2e27d513152faa998b2b215276799621dd611b2324d341fe4ea812ca122bea",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a412dfa636a0e6a64ec6625499fc7613991c395a0bb1362fd652f1a86ac60785",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a4cb89f83994be65a8354ab0008778b88cb1d277e27ddfe1f02d9884686bcbe2",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a6657047d03ef09467905c3a2a59a4158cf0913e07a0772b55b0595aed3c258d",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a73c1aa823fce32e79a6bdf047faded1b284ef69681dd8c890e8a796cc3e9fc0",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/d57f65f1d806a678aa67b57ccb635a8f7fbee6754d48caf945b67a5b58df3036",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/f1c258434e994b17aedcae8d7ce29915884094884cf65fb4c2cefdad2037e9a0",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/result",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/sbom"
      ],
      "profileConformance": [
        "build",
        "core",
        "software"
      ]
    },
    {
      "type": "software_File",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/documents/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c",
      "name": "b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c.spdx.json",
      "contentType": "application/spdx+json",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "c82446d1daecce32fcfc11cc5d57752b49ab3f6ec5bb088070f4f244e64fef7b"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/environment/bootstrap",
      "name": "bootstrap environment",
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/inputs/deb/dpkg/1/amd64/f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56",
      "name": "dpkg",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation",
      "software_packageUrl": "pkg:deb/debian/dpkg@1?arch=amd64",
      "software_packageVersion": "1"
    },
    {
      "type": "software_File",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/inputs/file/pkgs%2Ffixture%2Fbuild.yml/e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b",
      "name": "pkgs/fixture/build.yml",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "software_File",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/manifest",
      "name": "glbx output manifest",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation",
      "software_primaryPurpose": "manifest"
    },
    {
      "type": "software_File",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/control%3Afixture",
      "name": "control:fixture",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "8e9e15289a973e1215bd5cec4f8133558df06ab10a06e9439311f8794b2141c9"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/fixture.deb",
      "name": "fixture.deb",
      "verifiedUsing": [
        {
          "type": "Hash",
          "algorithm": "sha256",
          "hashValue": "2df8489d2a625c46969f0199ed9385b48c556268ab269b8aa871132607f6f45a"
        }
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation",
      "software_packageUrl": "pkg:deb/gardenlinux/fixture@1?arch=all",
      "software_packageVersion": "1"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/01199ab04800adb706027aaa29e06ca53d313580e9c159da15038e60cec35dc9",
      "relationshipType": "configures",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/inputs/file/pkgs%2Ffixture%2Fbuild.yml/e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/1f012fac8b6961b3a90cdbaa0637627fa96e05cd51f4b40b2c822f685fe95803",
      "relationshipType": "usesTool",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/inputs/deb/dpkg/1/amd64/f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/2496ff20fa24d300f629873412ace80a4220fd0402c2f695078e2a31ae9fce03",
      "relationshipType": "copiedTo",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/fixture.deb"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/32f14498ec8fb00277a3c84d568b9daa0d98dce201baff2cbacfd19aad6a5709",
      "relationshipType": "hasOutput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/control%3Afixture"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/43edee2788cc7c89226c37c181d14bf452eb9b0ffc3da7ce261b4b60c245f78e",
      "relationshipType": "usesTool",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/environment/bootstrap"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/48148bcf7ba774cf728f5923d3f73a8cf18708c8035cae5bbc36ea5a81e87e21",
      "relationshipType": "hasMetadata",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/result",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/manifest"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/4c6ef3711148e4c8b72031230cfb918a441f4f8d13719c34bed8f81c2c40654b",
      "relationshipType": "copiedTo",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/control%3Afixture",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/control%3Afixture"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/6cb90885fc0c1395983ad4e2b69f38af550b934cb3cb04d443b4b9ae7cef619b",
      "relationshipType": "contains",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/result",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/fixture.deb"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/8b2e27d513152faa998b2b215276799621dd611b2324d341fe4ea812ca122bea",
      "relationshipType": "hasMetadata",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/fixture.deb",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/control%3Afixture"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "Relationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a412dfa636a0e6a64ec6625499fc7613991c395a0bb1362fd652f1a86ac60785",
      "relationshipType": "contains",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/environment/bootstrap",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/inputs/deb/dpkg/1/amd64/f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a4cb89f83994be65a8354ab0008778b88cb1d277e27ddfe1f02d9884686bcbe2",
      "relationshipType": "hasInput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/fixture.deb"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a6657047d03ef09467905c3a2a59a4158cf0913e07a0772b55b0595aed3c258d",
      "relationshipType": "hasInput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c/outputs/control%3Afixture"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a73c1aa823fce32e79a6bdf047faded1b284ef69681dd8c890e8a796cc3e9fc0",
      "relationshipType": "invokedBy",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/agent"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/d57f65f1d806a678aa67b57ccb635a8f7fbee6754d48caf945b67a5b58df3036",
      "relationshipType": "hasOutput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/fixture.deb"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "LifecycleScopedRelationship",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/f1c258434e994b17aedcae8d7ce29915884094884cf65fb4c2cefdad2037e9a0",
      "relationshipType": "hasInput",
      "scope": "build",
      "from": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
      "to": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/inputs/file/pkgs%2Ffixture%2Fbuild.yml/e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "software_Package",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/result",
      "name": "debian-binary-pkg:fixture:amd64",
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation"
    },
    {
      "type": "software_Sbom",
      "spdxId": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/sbom",
      "rootElement": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/result"
      ],
      "creationInfo": "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/creation",
      "element": [
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/agent",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/build",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/documents/b8f87ed256adc2fcc87976db4f745a5d6dba21135f499d7721b3d03a6b5bb73c",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/environment/bootstrap",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/inputs/deb/dpkg/1/amd64/f64938a594f098303a7c9dc39e257ec8a5e988934cc2f11644bc48bc83b3ad56",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/inputs/file/pkgs%2Ffixture%2Fbuild.yml/e0308841133590d41d9caa87a7ec0640f6dae5ae014d9d658413a3983fa56a6b",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/manifest",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/control%3Afixture",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/outputs/fixture.deb",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/01199ab04800adb706027aaa29e06ca53d313580e9c159da15038e60cec35dc9",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/1f012fac8b6961b3a90cdbaa0637627fa96e05cd51f4b40b2c822f685fe95803",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/2496ff20fa24d300f629873412ace80a4220fd0402c2f695078e2a31ae9fce03",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/32f14498ec8fb00277a3c84d568b9daa0d98dce201baff2cbacfd19aad6a5709",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/43edee2788cc7c89226c37c181d14bf452eb9b0ffc3da7ce261b4b60c245f78e",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/48148bcf7ba774cf728f5923d3f73a8cf18708c8035cae5bbc36ea5a81e87e21",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/4c6ef3711148e4c8b72031230cfb918a441f4f8d13719c34bed8f81c2c40654b",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/6cb90885fc0c1395983ad4e2b69f38af550b934cb3cb04d443b4b9ae7cef619b",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/8b2e27d513152faa998b2b215276799621dd611b2324d341fe4ea812ca122bea",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a412dfa636a0e6a64ec6625499fc7613991c395a0bb1362fd652f1a86ac60785",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a4cb89f83994be65a8354ab0008778b88cb1d277e27ddfe1f02d9884686bcbe2",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a6657047d03ef09467905c3a2a59a4158cf0913e07a0772b55b0595aed3c258d",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/a73c1aa823fce32e79a6bdf047faded1b284ef69681dd8c890e8a796cc3e9fc0",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/d57f65f1d806a678aa67b57ccb635a8f7fbee6754d48caf945b67a5b58df3036",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/relationships/f1c258434e994b17aedcae8d7ce29915884094884cf65fb4c2cefdad2037e9a0",
        "urn:spdx:gardenlinux.io/glbx/sboms/a31495b6e689b0fe55e9ca6946eb263646514a2b0a692fb2bbdc8be19631af3e/result"
      ],
      "software_sbomType": [
        "build"
      ]
    }
  ]
}
```

## See also

- The output manifest these documents are keyed on, and the reserved
  `.glbx-provenance` output that keeps distinct builds distinctly addressed:
  [the artifact model](./artifact-model.md#the-output-manifest) and
  [the engine implementation](../impl/engine.md#the-provenance-output-and-manifest-uniqueness).
- The object store that holds each serialized document as a blob and each SBOM map
  entry: [object-store.md](./object-store.md).
- The OCI tag and wrapper-manifest conventions the SBOM OCI layout builds on:
  [remote-cache.md](./remote-cache.md).
- The publish step SBOM publication rides on, and the graph export it reuses:
  [distributed-builds.md](./distributed-builds.md).
- The artifact kinds whose builds each document describes:
  [package-build.md](./package-build.md) and
  [image-assembly.md](./image-assembly.md).

## References

- SPDX 3.0.1:
  [JSON-LD serialization](https://spdx.github.io/spdx-spec/v3.0.1/serializations/),
  [Build profile](https://spdx.github.io/spdx-spec/v3.0.1/model/Build/Build/),
  [relationship vocabulary](https://spdx.github.io/spdx-spec/v3.0.1/model/Core/Vocabularies/RelationshipType/),
  [`SpdxDocument`](https://spdx.github.io/spdx-spec/v3.0.1/model/Core/Classes/SpdxDocument/),
  [`ExternalMap`](https://spdx.github.io/spdx-spec/v3.0.1/model/Core/Classes/ExternalMap/),
  [`CreationInfo`](https://spdx.github.io/spdx-spec/v3.0.1/model/Core/Classes/CreationInfo/).
- Debian package metadata:
  [deb-control(5)](https://manpages.debian.org/unstable/dpkg-dev/deb-control.5.en.html),
  [Debian PURL type](https://github.com/package-url/purl-spec/blob/main/types/deb-definition.json).
- Distribution:
  [OCI 1.1 image manifest](https://github.com/opencontainers/image-spec/blob/v1.1.1/manifest.md),
  [OCI annotations](https://github.com/opencontainers/image-spec/blob/v1.1.1/annotations.md),
  [ORAS pull](https://oras.land/docs/commands/oras_pull/),
  [SPDX JSON media type](https://www.iana.org/assignments/media-types/application/spdx+json),
  [JSON Canonicalization Scheme (RFC 8785)](https://www.rfc-editor.org/rfc/rfc8785).
