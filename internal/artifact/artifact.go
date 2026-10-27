package artifact

import (
	"context"
	"os"

	"github.com/gardenlinux/glbx/internal/objstore"
)

// idSeed is the global identity-scheme seed, folded as the first element of
// every artifact identity. It namespaces the whole cache: changing it
// invalidates every cache entry at once. It is an arbitrary string — the
// 128-bit hex literal is just a conveniently unguessable value, not a format
// requirement. Regenerate (and accept a full cache miss) only when the shape of
// how identities are composed changes system-wide:
//
//	openssl rand -hex 16
//
// A per-artifact-type change does not touch this; each type carries its own
// seed. $GLBX_ID_SEED_OVERRIDE replaces it at runtime to get an independent
// cache namespace without editing the source.
const idSeed = "6a92ce0bffa008c51c6fe87e0143664a"

// IDSeed returns the global identity seed, honoring $GLBX_ID_SEED_OVERRIDE. It
// is the first element every artifact type folds into its identity.
func IDSeed() string {
	if override := os.Getenv("GLBX_ID_SEED_OVERRIDE"); override != "" {
		return override
	}
	return idSeed
}

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
	// ShortName is the display label for the node: the unique part only (the
	// source or binary package name, or "rootfs"), with no type prefix. Unlike
	// Key it is not guaranteed globally unique — it is for human-facing output,
	// not identity.
	ShortName() string
}
