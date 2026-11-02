# Repository access, import, and lock

The front half of the pipeline: fetching from an APT archive, importing a
source package, and pinning build-time tooling. These stand on their own, built
only on the foundations — the build engine and sandbox layer on top of them.

## `internal/provider`

Import and lockfile generation are pluggable operations. Each is implemented by
a provider registered under a namespace string (`debian`, `rootfs`, `pkg`,
`debian-kernel`); the command layer parses a `<namespace>:<arg>` target, selects
the provider, and hands it an `OpContext` (the logger-bearing context, the
object store, the working-tree root, and a validated parameter set) and the
target's single argument. Provider-specific options arrive on one opaque
`--params key=val,...` channel; each provider validates the set it receives,
rejecting unknown keys, and advertises what it accepts as a free-form
`ParamsHelp` string.

Unlike an artifact build — cache blobs in, cache blobs out — an import or
lockfile operation reads and writes the configuration tree (`pkgs/<name>/...`,
top-level `*-deps.yml`) as well as the cache, so that dual nature is carried on
the operation context rather than hidden behind an artifact. Two interfaces
capture the split:

- **`Importer`** fetches a package's pristine source into `pkgs/<name>/` plus
  the object-store blobs its sources reference, and records the import in the
  package's lineage. It returns a structured `ImportResult` — the machine
  contract the command layer branches on to decide whether to merge, and
  serializes as JSON in `--no-merge` mode. Import has no dispatch-by-config
  forwarder: it creates `pkgs/<name>/` and so runs before any `build.yml` exists,
  so its namespace is always explicit on the command line.
- **`LockGenerator`** resolves the external packages a build step needs, fetches
  them into the cache, and writes the pinned lock into the working tree. It
  returns only an error; its outcome is a log line and the written file on disk.

The concrete providers:

- **`debian`** — the plain Debian path. The lock generator reads
  `pkgs/<name>/src/debian/control`, resolves the build-dependency closure, and
  writes `build-deps.yml`; the importer wraps the import pipeline. Both run only
  glbx's own code — no container.
- **`rootfs`** — a zero-argument lock generator producing the image
  configuration-tooling `rootfs-deps.yml` (the Essential set plus `perl-base` and
  `mawk`). Same resolution machinery as `debian`, different roots and output path.
- **`debian-kernel`** — a container-backed lock generator for a package whose
  `debian/control` is a generated artifact. The committed control is stale with
  respect to any template or defines edit, so before locking, this provider
  regenerates it by running the package's own control-generation target inside a
  container it constructs from the Essential set plus the `gen_control_pkgs`
  declared in `build.yml`, copies the regenerated control back into the working
  tree, and then proceeds down the ordinary `debian` lock path. The fallback to
  the plain path is internal to the provider; the dispatch layer has no notion of
  it.
- **`pkg`** — a forwarding lock generator and the recommended developer entry
  point. It reads `pkg_type` from `pkgs/<name>/build.yml` and delegates to the
  concrete generator of that namespace, passing params through untouched, so a
  developer runs `lockfile pkg:<name>` without knowing whether a package is plain
  Debian or the kernel special case. Its `ParamsHelp` composes its delegates'
  help under each `pkg_type` heading. A `pkg_type` is required; the forwarder
  errors when it is absent.

A single `debianRepoConfig` helper validates and defaults the shared
Debian-family parameters (`repo`, `dist`, `snapshot`, `arch`, `cookie`,
`keyring`, `no-verify`, and the kernel's `gen-repo`/`gen-dist`/`gen-arch`) for
every Debian-family provider, so param validation lives in one place.

## `internal/stream`

Byte-stream primitives: SHA-256 readers/writers, subprocess-backed
decompressors (gzip/xz/bzip2/zstd with extension dispatch), tar
extract/create/list with deterministic creation flags, in-process `.deb`
ar-member extraction, and GPG verification. The subprocess helpers are pure
data transforms and run their tools directly on the host.

## `internal/debian/aptrepo`

Fetches and verifies a signed release index (GPG, or cleartext-strip when
verification is disabled), caches it in the object store keyed by the fetch
coordinates, and parses the release's path→hash map and date.

## `internal/importer`

Imports one source package: resolves the highest version from the signed
Sources index, downloads and hash-verifies each file into the store, and
extracts the packaging into the working tree. It handles the two committed
source formats and normalizes the legacy `1.0` format into a quilt-shaped tree
with a deterministic synthesized patch (timestamps stripped for
reproducibility). It writes `sources.yml`, recording each upstream archive by
hash and retrieval URL.

## `internal/lockfile`

Generates a package's build-tooling lock: fetch and verify the binary index,
read the package's declared build dependencies, resolve the closure, fetch and
hash-verify every resolved `.deb` into the store (16 in parallel), and write the
explicit per-architecture `build-deps.yml` — each tool recorded by name, exact
version, and per file its architecture, content hash, and retrieval URLs.
`GenerateRootfs` produces the image configuration-tooling lock in the same form.

## `internal/restore`

Fills a cold object store from the pins already recorded in the working tree.
For every source archive in `sources.yml` and every tooling `.deb` in
`build-deps.yml` for the target architecture, it checks whether the blob is
present and, if not, fetches it from the recorded URLs in preference order,
keeping the first whose bytes match the pinned hash. It is the read side of the
pins the import and lock steps write: no index fetch or resolution, just a
hash-verified download of what the tree already names.
