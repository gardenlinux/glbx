# Repository access, import, and lock

The front half of the pipeline: fetching from an APT archive, importing a
source package, and pinning build-time tooling. All usable before any build
engine or sandbox exists.

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
