# glbx

glbx — the Garden Linux Build eXecutor — builds Debian packages from source
inside hermetic sandboxes, validates that every binary's runtime closure is
itself built from source, and assembles the results into reproducible system
images. Every artifact is addressed by a content-derived identity, so a build
is a cache lookup and a rebuild is a guaranteed-identical result.

This book has two halves:

- **Concepts** — the design of record: what the system is and why it is shaped
  the way it is. Identity, the object store, the artifact model, source lineage,
  package building, image assembly, and the execution environment.
- **Implementation** — a per-component tour of the code under `internal/` and
  `cmd/`, for a reader working on glbx itself.
