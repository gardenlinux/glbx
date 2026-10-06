# Foundations

The dependency-free primitives everything else rests on.

## `internal/objstore` — identity and the object store

`Hash` is a validated 256-bit SHA-256 digest handled as lower-case hex; it is
the single value type used both as a blob's content address and as an
artifact's identity. `ConcatHash` folds an ordered tuple of parts into one hash
by SHA-256'ing each part to a fixed 32-byte frame and hashing the concatenation,
so the encoding is unambiguous and order-significant.

A local store is a directory of content-addressed `blobs/` and an
identity→manifest `map/`, both sharded by the first two hex characters of the
key. Blobs are written atomically (temp file, hash while writing, rename into
place). A read-only `Remote` interface serves blobs and map entries by hash; a
pull-through store composes a local store with a remote so that on a local miss
it fetches from the remote, writes the bytes locally (re-hashing to verify), and
re-reads locally — a caller always sees local data. Garbage collection takes a
caller-supplied keep-set, sweeps blobs against it, then sweeps the map to follow
the surviving blobs. The store protects nothing of its own — it is a pure
cache.

## `internal/dirhash` — directory hashing

`HashDirectory` reduces a tree to one SHA-256 that depends only on build-
relevant content: entry names in byte order, entry types, the single executable
bit, and file bytes. Owner, group, the rest of the mode, and timestamps are
excluded, so two checkouts of the same source hash identically. Traversal is
confined with `os.OpenRoot`.

## `internal/log` — structured logging

Leveled, component-tagged records delivered to a `Target`: a TTY-aware console
sink, an in-memory buffer with a tailing reader, a replay printer, and JSON
serialization. The active target rides on the context, so any code obtains a
component logger without threading one through every call.

## `internal/taskui` — progress UI

A flat task tracker (each task has a lifecycle state, timestamps, and its own
log buffer) with an interactive live overview, a non-interactive status ticker,
a plain dump, a Mermaid Gantt rendering, and whole-tracker serialization for
later replay.
