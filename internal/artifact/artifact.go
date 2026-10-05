package artifact

import (
	"context"

	"github.com/gardenlinux/glbx/internal/objstore"
)

// Output is a single named result produced by an artifact's Build().
// The Name is an opaque identifier — could be a filename, a qualified name
// like "control:libc6", or any other string. The engine stores/loads these
// generically via the manifest format: "<hash> <name>\n" per entry.
type Output struct {
	Name string
	Hash objstore.Hash
}

// Input references a specific named output from a dependency artifact.
// The engine resolves these before calling Build() — matching Source's outputs
// by Name to provide the actual blob hashes in BuildContext.Inputs.
type Input struct {
	Source Artifact
	Name   string
}

// BuildContext is passed to Build() with the object store and pre-resolved
// input hashes. The Inputs map is keyed by the Input.Name strings from
// the artifact's Inputs() method, with values being the resolved blob hashes.
type BuildContext struct {
	Ctx    context.Context
	Store  *objstore.Store
	Inputs map[string]objstore.Hash
}

type Artifact interface {
	Key() string
	Identity() (objstore.Hash, error)
	// Depends are build-order edges: an artifact's dependencies must be built
	// before it. Cycles among Depends are forbidden and rejected by the engine.
	Depends() []Artifact
	// Includes are closure-only edges: included artifacts contribute to the
	// transitive runtime/install closure of consumers but impose no build-order
	// constraint. Cycles among Includes are legal — the engine's cycle detector
	// ignores them. Two artifacts produced by the same source build that
	// reference each other (e.g. libssl3t64 ↔ openssl-provider-legacy) declare
	// the relationship via Includes; their actual co-production happens in the
	// shared parent source build, which IS a Depends.
	Includes() []Artifact
	Inputs() []Input
	Build(ctx BuildContext) ([]Output, error)
	// OutputRefs returns the blob references for an artifact that has already
	// been built, without building. manifest is the hash of the map's target
	// blob (the local manifest / entrypoint); leaves are the output blobs it
	// references. Returns an error if the artifact has not been built (no map
	// entry), so graph-walking callers (GC, upload) can skip on error.
	OutputRefs(store *objstore.Store) (manifest objstore.Hash, leaves []objstore.Hash, err error)
	String() string
}
