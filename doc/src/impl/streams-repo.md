# Byte streams and repository access

Two packages sit at the bottom of glbx's data-handling stack. `internal/stream`
is a toolbox of byte-stream primitives — hashing, decompression, tar, `.deb`
member extraction, GPG verification. `internal/debian/aptrepo` builds on it to
fetch and trust a Debian repository's signed release metadata. Neither package
knows anything about the artifact graph, the resolver, or the sandbox; they are
the plumbing that higher layers call to turn remote bytes into trusted, named
content.

Both are deliberately boring. They move bytes, verify bytes, and hash bytes.
That narrowness is what lets the rest of the system treat "fetch this source
archive" or "verify this release file" as a single reliable call.

## `internal/stream` — byte-stream primitives

The package doc on `process.go` sets the frame: these are *pure data
transforms* — bytes in, bytes out, or bytes to a target path. A decompressor
reads a compressed stream and produces a plaintext stream; a tar extractor reads
an archive and writes a directory; a hash reader reads bytes and yields a digest.
None of them mutates shared state, needs a view of the filesystem beyond the one
path it is handed, or performs an action that must be contained. That is why
none of them runs inside the build sandbox. The sandbox exists to confine
package *build* and *install* steps — arbitrary maintainer scripts executing as
root against a rootfs. Fetching and decompressing a source tarball is not that:
it is a deterministic transform on a byte stream, and it runs directly on the
host like any other library call.

Several of these helpers do, however, shell out to host tools rather than use Go
libraries. That is a conscious split, covered per helper below, and it does not
contradict the "no sandbox" point: a subprocess that reads stdin and writes
stdout is still a pure transform — it just happens to be implemented by `xz` or
`tar` instead of Go code.

### Hashing — `hash.go`

The hashing helpers come in two shapes. The streaming wrappers, `HashReader`
and `HashWriter`, tee every byte through a `hash.Hash` as it passes:
`NewHashReader(r)` returns a reader that behaves exactly like `r` but
accumulates a SHA-256 digest of everything read, recoverable at any point via
`Sum()` (hex-encoded). `HashWriter` is the write-side mirror. These let a caller
hash a stream *while* it is being copied somewhere else — no second pass, no
buffering the whole payload to hash it afterwards.

The one-shot helpers hash a complete input and return the hex digest directly:

- `SHA256Reader(r)` / `SHA256Bytes(data)` — SHA-256, the identity hash used
  throughout the object store and the content-addressed graph.
- `SHA1Reader(r)` / `SHA1Bytes(data)` — SHA-1, which exists for exactly one
  reason: `snapshot.debian.org` addresses files by their SHA-1 digest. `SHA1Bytes`
  computes the key that `aptrepo.SnapshotURL` turns into a retrieval URL. SHA-1 is
  never used as a trust or identity hash in glbx; it is purely the snapshot
  archive's addressing scheme.

All five return lowercase hex strings. There is no central interface here — these
are free functions and two small wrapper types.

### The subprocess-helper pattern — `process.go`, `decompress.go`

The decompressors do not use Go's compression packages. They spawn the host's
command-line tools and pipe bytes through them. The machinery lives in
`process.go` as `procReadCloser`, and it is worth understanding because the same
pattern recurs for tar.

`newProcReadCloser(inputReader, name, args...)` starts an `exec.Cmd` with its
stdin wired to `inputReader` and its stdout exposed as a pipe. The returned value
is an `io.ReadCloser`:

- `Read` reads from the process's stdout pipe — so decompression is *streaming*,
  not buffered. The subprocess produces plaintext as the caller consumes it.
- `Close` closes the stdout pipe (signalling end-of-read) and then `Wait`s for
  the process. Crucially, **the process's exit status is only observed at
  `Close`.** A decompressor that fails partway — truncated input, bad magic —
  surfaces its error when the caller closes the reader, not necessarily at the
  `Read` that hit the end of good data. Stderr is captured into a buffer and
  folded into the returned error, so a failure reads as `stream: <tool> exited
  with error: ... \nstderr: ...`. Callers must therefore check the error from
  `Close`, not just `Read` — a `defer decomp.Close()` that drops its return value
  can mask a corrupt stream.

On top of this, `decompress.go` is a thin dispatch layer. Four decompressors,
each a one-liner over `newProcReadCloser`:

| Function | Spawns |
|---|---|
| `XZDecompress`    | `xz -d -c` |
| `GzipDecompress`  | `gzip -d -c` |
| `Bzip2Decompress` | `bzip2 -d -c` |
| `ZstdDecompress`  | `zstd -d -c` |

`AutoDecompress(r, filename)` selects among them by filename suffix — `.xz`,
`.gz`, `.bz2`, `.zst` — and errors on anything else. It dispatches on the
*filename*, not on sniffed content; the name is what a Debian index or source
entry carries, so this is the natural key. There is no compression side in this
package — glbx reads compressed Debian artifacts, it never writes them.

The reason to shell out rather than link Go libraries: these are exactly the
tools a Debian system already ships and trusts, their behaviour matches what
produced the archives, and keeping them as subprocesses keeps the dependency
surface of the Go binary small. The cost is a hard runtime dependency on those
binaries being on `PATH` — a missing `zstd` is a `start` failure from
`newProcReadCloser`, surfaced immediately.

### Deterministic tar — `tar.go`

`tar.go` is the other shell-out, and the create path is where determinism
matters. `TarCreate(dir, w)` archives a directory to a writer with a fixed set
of flags:

```
tar --mtime=@0 --numeric-owner --sort=name -c -C <dir> .
```

Every flag is there to kill a source of nondeterminism:

- `--mtime=@0` pins every entry's modification time to the epoch, so the archive
  bytes do not depend on when the files were written.
- `--numeric-owner` records numeric uid/gid rather than resolving them to names
  against the host's `/etc/passwd`, so the archive does not depend on the host's
  user database.
- `--sort=name` emits entries in a stable byte-wise order rather than directory
  iteration order.

The consequence is that archiving the same tree twice produces the same bytes,
which produces the same SHA-256, which is what the content-addressed graph
requires. If `TarCreate` were nondeterministic, identical inputs would get
different identities and the cache would never hit. Any change to these flags is
a change to artifact identity and must be weighed as such.

The extract and inspect helpers have no determinism requirement — they consume
archives, they do not produce identities:

- `TarExtract(r, targetDir, stripComponents)` — `tar -C <dir> -x`, with
  `--strip-components=N` when `N > 0`. A terminal sink: it reads to EOF and
  blocks until extraction completes.
- `TarExtractCompressed(r, filename, targetDir, stripComponents)` — the common
  composition: `AutoDecompress` by filename, then `TarExtract`. This is the entry
  point `DebExtractData`'s caller pairs with.
- `TarList(r)` — `tar -t`, returning the entry names as a slice.
- `TarExtractFile(r, path)` — `tar -xO <path>`, returning one named member's
  contents as bytes.

All of these capture stderr and fold it into their errors, the same discipline
as the decompressors.

### In-process `.deb` extraction — `deb.go`

The one place `internal/stream` does *not* shell out is `.deb` unpacking. A
`.deb` is an `ar` archive containing (typically) `debian-binary`,
`control.tar.*`, and `data.tar.*`. `DebExtractData(r)` parses the `ar` container
in pure Go and returns a reader for the `data.tar.*` member plus its filename, so
the caller can hand both to `TarExtractCompressed` (the name selects the
decompressor).

The `ar` format is trivial enough that a parser is less friction than another
subprocess dependency:

- The archive opens with the magic `!<arch>\n` (8 bytes), which `DebExtractData`
  checks.
- Each member has a fixed 60-byte header: a 16-byte name, timestamp, uid, gid,
  mode, a 10-byte decimal size, and a two-byte terminator `` `\n ``, which
  `readArHeader` validates. Member names are space-padded and carry a trailing
  `/`, both trimmed off.
- Members are 2-byte aligned, so an odd-sized member is followed by a padding
  byte — `DebExtractData` skips `size + 1` when the size is odd.

It walks members until it finds one whose name begins with `data.tar`, reads that
member fully into memory, and returns a `bytes.Reader` over it. Reading the
member into memory (rather than returning a bounded reader over the underlying
stream) makes the result self-contained: the caller can consume it independently
of the original reader's position. For Debian `.deb` payloads this is a sound
trade — individual data members are not large enough to make the buffering a
concern, and the simplicity is worth it. Members before `data.tar.*` are skipped
by seeking past their bytes, never parsed.

### GPG clear-signed verification — `gpg.go`

`gpg.go` verifies signatures using the host's `gpgv` and then, for clear-signed
documents, strips the signature envelope to recover the payload. Three functions:

- `GPGVerify(keyringPath, signaturePath, dataPath)` — a detached-signature check:
  `gpgv --keyring <k> <sig> <data>`. Pure verification of files already on disk.
- `GPGVerifyClearSigned(keyringPath, signedData)` — the one that matters for
  repository access. It reads the clear-signed document, writes it to a temp file
  (because `gpgv` wants a path), runs `gpgv --keyring <k> <tmp>` to verify the
  inline signature, and on success returns the stripped payload. On verification
  failure it returns an error carrying `gpgv`'s stderr and no payload.
- `ExtractClearSignedPayload(data)` — strips the envelope *without* verifying.
  This is the no-trust path: when a caller has an out-of-band reason to trust the
  bytes (or explicitly opts out of verification), it still needs the raw payload
  pulled out of the PGP framing.

The envelope handling (`extractClearSignedPayload`) implements the OpenPGP
cleartext format precisely:

- locate `-----BEGIN PGP SIGNED MESSAGE-----`;
- skip the header block (the `Hash:` line and friends) up to the first blank
  line, handling both `\n\n` and `\r\n\r\n`;
- take everything up to `-----BEGIN PGP SIGNATURE-----` as the payload, trimming
  the trailing newline before the marker;
- **un-dash-escape**: the cleartext format prefixes any payload line that would
  otherwise look like a marker with `"- "`, so every line beginning `"- "` has
  those two characters removed.

The separation between verify-then-strip and strip-only is the hinge that
`aptrepo` swings on: the same payload-extraction logic serves both the trusted
and the explicitly-untrusted fetch, so the two paths produce byte-identical
payloads and differ only in whether `gpgv` ran.

## `internal/debian/aptrepo` — signed release metadata

`aptrepo.go` is the shared front door to a Debian repository's release metadata.
Two consumers need it: the importer, which walks `Sources` indexes, and the
lockfile generator, which walks `Packages` indexes. Both must first fetch the
repository's `InRelease` file, establish trust in it, and then read the
content-addressed hashes of the specific index files they care about. `aptrepo`
owns that shared prefix.

It handles **inline-signed `InRelease` only**. The older detached form — a plain
`Release` file alongside a separate `Release.gpg` detached signature — is not
implemented here. Modern Debian repositories publish `InRelease`, and that is the
single path this package supports.

### Fetching and trusting — `FetchInRelease`

`FetchConfig` carries the fetch coordinates. `Store`, `RepoURL`, and `Dist` are
required; the rest are optional:

- `Cookie` — if non-empty, keys a blob-cache entry (see below).
- `Keyring` — path to a GPG keyring for verification; ignored when `NoVerify`.
- `NoVerify` — skip signature verification, strip the envelope only. For callers
  with an out-of-band trust path.
- `HTTPClient` — defaults to `http.DefaultClient`.
- `Component` — log component tag, defaults to `log.Fetch`.

`FetchInRelease(ctx, cfg)` returns the verified-and-stripped Release payload (the
deb822 stanza body). Its logic:

1. Obtain the raw `InRelease` bytes via `loadOrDownloadInRelease` (cache or
   HTTP).
2. If `!NoVerify` and a `Keyring` is set, call
   `stream.GPGVerifyClearSigned` — verify and strip.
3. Otherwise call `stream.ExtractClearSignedPayload` — strip without verifying.

So verification happens only when a keyring is present *and* `NoVerify` is false;
in every other case the bytes are trusted and merely unwrapped. This is the one
decision point in the package, and it maps directly onto the two `stream.gpg.go`
entry points.

The URL is constructed as `<RepoURL>/dists/<Dist>/InRelease`. `httpGet` is a
plain `client.Get` that treats any non-200 status as an error and reads the body
into memory.

### The cookie cache

`loadOrDownloadInRelease` is where the object store earns its keep. When `Cookie`
is empty, it always downloads — no caching. When `Cookie` is set, it uses the
object store as a key-value cache for the raw `InRelease` bytes:

- The cache key is computed by `inReleaseCacheKey(cookie, repoURL, dist)` as
  `objstore.HashBytes` of the string `"InRelease:" + repoURL + ":" + dist + ":" +
  cookie`. So the key folds together the repository URL, the distribution, and
  the caller's cookie — two fetches collide in the cache only if all three match.
- On a hit, the key is looked up in `Store.Map` to get a blob hash, and the blob
  is `Open`ed and read from `Store.Blobs`. On a miss, the file is downloaded,
  `Store`d as a blob, and the key→blob mapping `Set` in `Store.Map` (a failed
  `Set` is logged as a warning, not fatal — the fetch still succeeds).

The cookie is supplied by the caller and should encode whatever trust or
freshness context makes a cached copy reusable — the release date is the natural
choice. "Same repository, same dist, same release date" means "same bytes," so
repeated invocations within a build skip the mirror round-trip entirely. The
cache stores the *raw, still-signed* `InRelease`, so verification still runs on a
cache hit — caching does not short-circuit trust.

### Parsing the payload

Once `FetchInRelease` returns the stripped payload, three parsers read fields out
of it via the deb822 reader:

- `ParseReleaseHashes(payload)` — reads the `SHA256` field (a multi-line list of
  `<hash> <size> <path>` records) and returns a `map[path]hash`. It keys on the
  path (field index 2) and takes the hash (field index 0), skipping malformed
  lines, and errors if the `SHA256` field is absent or yields no entries. This is
  how a consumer resolves "the `Sources` index for component X" into a concrete
  content hash it can fetch and verify.
- `ParseReleaseDate(payload)` — returns the `Date` field verbatim (an
  RFC1123-style string such as `Sat, 01 Mar 2026 00:00:00 UTC`), or empty string
  with no error when absent. This is the value most callers feed back in as the
  cache cookie.

### Snapshot addressing

The last piece bridges to `snapshot.debian.org`, which serves any file Debian
has ever published, addressed by content:

- `DefaultSnapshotBase` is `https://snapshot.debian.org/file` — the
  hash-addressed file endpoint. Files there are keyed by SHA-1 digest,
  independent of suite or timestamp, which makes a snapshot URL a stable second
  source for any file already pinned by content.
- `SnapshotURL(base, sha1, name)` builds the retrieval URL as
  `<base>/<sha1>/<name>`, with `base` defaulting to `DefaultSnapshotBase` when
  empty and `name` percent-escaped as the trailing path segment so the download
  keeps a sensible filename. The `sha1` is exactly what `stream.SHA1Bytes`
  computes over the file's bytes.

Together with `SHA1Bytes`, this is how glbx turns a content hash into a durable
download URL: a file that has rotated out of a live mirror is still retrievable
from the snapshot archive as long as its SHA-1 is known.
