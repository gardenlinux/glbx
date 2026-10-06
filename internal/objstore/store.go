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

// Store is the object store: content-addressed blobs and the identity-to-
// manifest map over one root directory. The two fields are interfaces so a store
// can be either pure-local or a pull-through cache backed by a remote, without
// consumers knowing which.
type Store struct {
	root  string
	Blobs BlobStore
	Map   MapStore
}

// Open opens or creates a pure-local store at root, or at DefaultRoot() if root
// is empty. The root is resolved to an absolute path so the store can be
// consulted from inside a sandbox whose working directory differs from the
// host's.
func Open(root string) (*Store, error) {
	return open(root, nil)
}

// OpenWithRemote opens a store whose reads fall through to remote, filling the
// local layer on a miss. A nil remote yields a pure-local store.
func OpenWithRemote(root string, remote Remote) (*Store, error) {
	return open(root, remote)
}

func open(root string, remote Remote) (*Store, error) {
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

	s := &Store{root: root}
	if remote == nil {
		s.Blobs = blobs
		s.Map = mapStore
		return s, nil
	}
	ptBlobs := &pullThroughBlobs{local: blobs, remote: remote}
	s.Blobs = ptBlobs
	s.Map = &pullThroughMap{local: mapStore, blobs: ptBlobs, remote: remote}
	return s, nil
}

// Root returns the filesystem root of the store.
func (s *Store) Root() string {
	return s.root
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
