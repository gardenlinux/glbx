package objstore

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultRoot returns the object-store root: $GLBX_CACHE if set, otherwise
// ~/.cache/glbx, falling back to a temp directory if no home is known.
func DefaultRoot() string {
	if env := os.Getenv("GLBX_CACHE"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "glbx-cache")
	}
	return filepath.Join(home, ".cache", "glbx")
}

// Store is the object store: a content-addressed blob store and an identity-to-
// manifest map. Both fields are interfaces, so a store can be local, backed by a
// registry, or a pull-through cache composing two others — consumers never know
// which. The implementations behind the interfaces hold all state; the wrapper
// holds none.
type Store struct {
	Blobs BlobStore
	Map   MapStore
}

// NewLocal opens or creates a local store at root, or at DefaultRoot() if root
// is empty. The root is resolved to an absolute path so the store can be
// consulted from inside a sandbox whose working directory differs from the
// host's.
func NewLocal(root string) (*Store, error) {
	if root == "" {
		root = DefaultRoot()
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving absolute path for %s: %w", root, err)
	}
	root = absRoot

	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating store root %s: %w", root, err)
	}

	blobs, err := newBlobs(filepath.Join(root, "blobs"))
	if err != nil {
		return nil, fmt.Errorf("initializing blobs: %w", err)
	}
	mapStore, err := newMapStore(filepath.Join(root, "map"), blobs)
	if err != nil {
		return nil, fmt.Errorf("initializing map: %w", err)
	}
	return &Store{Blobs: blobs, Map: mapStore}, nil
}

// NewRegistry opens a store backed by the OCI registry at ref
// ("host[:port]/repo"). Its reads resolve against the registry; its write, path,
// and sweep operations are refused. insecure selects plaintext HTTP for a local
// test registry. A registry store is only useful composed into a pull-through.
func NewRegistry(ref string, insecure bool) (*Store, error) {
	reg, err := parseRegistry(ref, insecure)
	if err != nil {
		return nil, err
	}
	return &Store{Blobs: &remoteBlobs{reg: reg}, Map: &remoteMap{reg: reg}}, nil
}

// NewPullThrough composes a near store and a far store into a pull-through
// cache: reads miss in near fall through to far and are written back into near,
// so the next read is a near hit; writes and maintenance act on near only. It
// requires only that both arguments satisfy the store interfaces — neither has
// to be a local or a registry specifically.
func NewPullThrough(near, far *Store) *Store {
	blobs := &pullThroughBlobs{local: near.Blobs, remote: far.Blobs}
	return &Store{
		Blobs: blobs,
		Map:   &pullThroughMap{local: near.Map, blobs: blobs, remote: far.Map},
	}
}

// GC reclaims every blob not named in keep, then sweeps the map to drop any
// entry whose target blob no longer exists. Returns the counts deleted.
func (s *Store) GC(keep map[Hash]struct{}) (blobs, entries int, err error) {
	blobs, err = s.Blobs.Sweep(keep)
	if err != nil {
		return blobs, 0, err
	}
	entries, err = s.Map.SweepFollowingBlobs()
	return blobs, entries, err
}
