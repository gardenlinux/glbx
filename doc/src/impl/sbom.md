# SBOM generation

The [SBOMs](../concepts/sbom.md) concept chapter fixes *what* glbx emits: one
SPDX 3.0.1 JSON-LD document per artifact result, the element and relationship
shapes, the URI scheme, and the cross-document import contract. This chapter is
the code tour — how the generator is structured, where the writers live, and the
one invariant everything leans on.

The generator lives in `internal/build/sbom_*.go`, plus the storage and seed
plumbing in `internal/artifact/sbom.go`. The engine calls it after a node's
manifest is stored; the result is written to the object store as an ordinary blob
under a map key, exactly as described in the concept chapter's storage section.

## The one invariant: IDs are a pure function of (namespace, M, path)

Every SPDX element glbx mints has an ID of the form
`urn:spdx:<A>/glbx/sboms/<M>/<path>`, where `A` is the SBOM namespace and `M` is
the producing artifact's output-manifest hash. `spdxDoc.spdxID(path)`
(`sbom_spdx.go`) builds the local form; `spdxDoc.foreignID(producer, path)` builds
the form a *different* producer (manifest hash `N`) would mint in the same
namespace.

That determinism is the whole basis of cross-document references. A consumer that
needs to point at a producer's output does not invent an ID or copy one it was
handed — it recomputes the exact URI the producer would have minted, from the
producer's manifest hash and the known element path. Consumer and producer never
coordinate; they agree because they run the same pure function. `segEscape`
percent-encodes the arbitrary-byte path segments (output names, file paths) so a
name like `control:fixture` survives as one identifiable segment
(`control%3Afixture`), identically on both sides.

## The opinionated helper library

`sbom_spdx.go` is a deliberately narrow, SPDX-opinionated builder — not a general
SPDX library. It encodes exactly the element shapes, ID scheme, profiles and JSON
serialization the concept chapter fixes, and nothing else. A writer works in
Go-native terms ("this build has these inputs, these tools, these outputs, these
relationships") and never touches raw JSON-LD.

`spdxDoc` accumulates a `@graph` of element objects plus bookkeeping for the
document-level `element`/`import` lists. Its surface is a set of element
constructors and one relationship recorder:

- **Element constructors** each append one element and return its ID: `addAgent`,
  `addArtifactBuild`, `debPackage`, `sourcePackage`, `fileElement` / `outputFile`,
  `gitPackagingElement`, `environment`, `payload`. The ID is the handle a writer
  passes to `relate`.
- **`relate(from, relType, to, scope)`** records one relationship. A non-empty
  `scope` makes it a `LifecycleScopedRelationship` (build-scoped facts, including
  `hasStaticLink`); an empty scope makes it a plain `Relationship` (containment,
  byte copies, `hasMetadata`). The relationship's own ID is a content hash folded
  from its fields, so identical edges dedup and the ID is reproducible.
- **`importElement(producer, path, docChecksum)`** is the cross-document
  primitive. It does *not* mint a local body for the foreign element — the
  referenced element lives in the producer's document. It mints a `documents/<N>`
  `software_File` pinning the producer's whole serialized document by checksum,
  adds an `ExternalMap` to the consumer's `SpdxDocument.import` wiring the foreign
  element's ID to that defining file, and returns the foreign ID for the writer to
  target in an edge.
- **`marshal`** finalizes: it synthesizes the shared `CreationInfo`, the
  `software_Sbom` (whose `rootElement` is the `artifact-build`), and the
  `SpdxDocument` (element + import lists, profile conformance), then serializes the
  `@graph` deterministically.

`addArtifactBuild` is where the build-and-result merge lives: SPDX forbids one
element being both a `build_Build` and a `software_Package`, so the single
`build_Build` element — a Core element — carries the result `name` directly and is
the SBOM root. There is no separate result package. `producerManifest` resolves a
dependency artifact to its output-manifest hash `N` and its SPDX document checksum
`D` (via the artifact's `OutputRefs` and the SBOM map key), the two values
`importElement` needs.

## Per-kind writers

Each artifact kind's `SBOM` method lives in its own file and only expresses its
own inputs, environment and outputs; everything else is the shared library.

- **`sbom_source.go`** — `writeSourceBuildSBOM` drives both `debian-pkg-build`
  (`DebianPkgBuild.SBOM`) and, through the embedded inner build,
  `tooling-pkg-build` (`ToolingPkgBuild.SBOM`); the two differ only in the build
  type and in where the build-container packages come from. It also hosts
  `emitPackageOutputs` (the `<bin>.deb` + `control:<bin>` elements with their
  `hasOutput` / `hasMetadata` / `hasStaticLink` edges), reused by the binary
  writer, and `staticBuiltUsing`, which parses a control stanza's `Built-Using` /
  `Static-Built-Using` fields into the static-link declarations.
- **`sbom_binary.go`** — `debianBinaryPkg.SBOM` imports the source build's selected
  `.deb` and control, re-emits them under the validation manifest, and records the
  `copiedTo` edges; it describes the install-check bootstrap container.
- **`sbom_rootfs.go`** — `Rootfs.SBOM` records `rootfs.tar.gz → expandsTo →
  payload`, imports each shipped package from its producing binary-package document
  into `payload`'s `contains`, and describes the discarded Layer-1 configuration
  tooling under the `layer1` environment.
- **`sbom_image.go`** — `Image.SBOM` imports the rootfs payload and any included
  images (the `contains` edges that record, for example, a disk embedding an
  initrd), describes the assembly container, and records the recipe and literal
  inputs.

Each writer recomputes the package sets it describes the same way `Build` assembled
them (`recomputeChrootPkgs`, `recomputeInstallCheckChroot`, the rootfs
install/Layer-1 split), so the SBOM reflects the actual build, and tolerates a
missing lockfile for degenerate inputs rather than failing.

## Git packaging references

`sbom_git.go` resolves the VCS coordinates of a package's `src/debian` directory
for the packaging element's `externalRef`: the repository
(`GLBX_SBOM_SOURCE_REPO`, else the checkout's `origin`, else the repository root
path), the `HEAD` commit, and the repo-relative subdirectory. It is best-effort —
outside a git checkout the commit is empty and the locator degrades gracefully.

## The `SBOM` interface and engine wiring

The `Artifact.SBOM` method (`internal/artifact/artifact.go`) takes an
`SBOMContext` carrying everything a writer needs that is not already on the
artifact: the store, the resolved build inputs, the emitted outputs, the identity,
the output-manifest hash the SBOM is keyed on, and the active SBOM namespace. An
artifact kind that produces no SBOM returns `nil`.

`internal/artifact/sbom.go` holds the storage side: the dedicated SBOM seed,
`SBOMNamespace` (`GLBX_SBOM_NAMESPACE`, default `localhost`), the `sbomMapKey`
fold, and `storeSBOM`, which the engine calls after `storeManifest`. The SBOM
document is written as an ordinary object-store blob and bound by a map entry
under the folded key; a `nil` SBOM is a no-op. This is local-store only —
publishing SBOMs to a remote is a separate step.
