package objstore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// Remote is the pull-through backend: a read-only store a local miss falls
// through to. A nil remote makes the store pure-local. The interface lives here
// so a backend can be injected without objstore depending on it.
type Remote interface {
	// PullBlobByDigest fetches a blob by its content hash. An error for which
	// errors.Is(err, os.ErrNotExist) holds (or any error) is treated as a miss;
	// the caller surfaces the original local not-found.
	PullBlobByDigest(h Hash) (io.ReadCloser, int64, error)
	// PullOutputManifest fetches the leaf outputs for an artifact identity.
	// ok=false means the identity is absent remotely.
	PullOutputManifest(identity Hash) (leaves []Output, ok bool, err error)
}

// Store is the object store: content-addressed blobs and the identity-to-
// manifest map over one root directory. An optional Remote turns reads into a
// pull-through cache that fills the local store on a miss.
type Store struct {
	root   string
	Blobs  *Blobs
	Map    *MapStore
	remote Remote
}

// Open opens or creates a store at root, or at DefaultRoot() if root is empty.
// The root is resolved to an absolute path so the store can be consulted from
// inside a sandbox whose working directory differs from the host's.
func Open(root string) (*Store, error) {
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

	return &Store{root: root, Blobs: blobs, Map: mapStore}, nil
}

// Root returns the filesystem root of the store.
func (s *Store) Root() string {
	return s.root
}

// SetRemote attaches, or clears with nil, a pull-through remote.
func (s *Store) SetRemote(r Remote) {
	s.remote = r
}

// HasRemote reports whether a pull-through remote is configured.
func (s *Store) HasRemote() bool {
	return s.remote != nil
}

// OpenBlob opens a blob, pulling it from the remote into the local store on a
// local miss. The returned bytes always come from the local store; the pull
// re-hashes on write, verifying the digest. With no remote this is Blobs.Open.
func (s *Store) OpenBlob(h Hash) (io.ReadCloser, error) {
	if s.Blobs.Has(h) || s.remote == nil {
		return s.Blobs.Open(h)
	}
	// A failed pull falls through to the local open, which yields the
	// canonical not-found error.
	_ = s.pullBlob(h)
	return s.Blobs.Open(h)
}

// EnsureBlob materializes a blob locally if absent by pulling it from the
// remote. It is a no-op when the blob is present or no remote is configured.
// Consumers that read a blob by filesystem path call this at their presence
// guard so those paths fall through too.
func (s *Store) EnsureBlob(h Hash) error {
	if s.Blobs.Has(h) || s.remote == nil {
		return nil
	}
	return s.pullBlob(h)
}

// pullBlob fetches h from the remote and stores it locally. The re-hash on
// write verifies the digest; a mismatch is reported and nothing is kept under
// the requested hash.
func (s *Store) pullBlob(h Hash) error {
	rc, _, err := s.remote.PullBlobByDigest(h)
	if err != nil {
		return err
	}
	defer rc.Close()
	got, err := s.Blobs.Store(rc)
	if err != nil {
		return err
	}
	if !got.Equal(h) {
		return fmt.Errorf("pull blob %s: remote returned digest %s", h, got)
	}
	return nil
}

// MapGet resolves an identity to its manifest-blob hash. On a local miss with a
// remote configured, it pulls the output manifest and every leaf blob,
// reconstructs the local manifest byte-identically, stores it, and sets the map
// entry — turning the miss into a permanent local hit. With no remote this is
// Map.Get.
func (s *Store) MapGet(identity Hash) (Hash, error) {
	if h, err := s.Map.Get(identity); err == nil {
		return h, nil
	} else if s.remote == nil {
		return Hash{}, err
	}

	leaves, ok, err := s.remote.PullOutputManifest(identity)
	if err != nil {
		return Hash{}, err
	}
	if !ok {
		return s.Map.Get(identity)
	}

	for _, leaf := range leaves {
		if err := s.EnsureBlob(leaf.Hash); err != nil {
			return Hash{}, fmt.Errorf("pull leaf %s: %w", leaf.Hash, err)
		}
	}

	manifestHash, err := s.Blobs.Store(strings.NewReader(SerializeManifest(leaves)))
	if err != nil {
		return Hash{}, fmt.Errorf("store reconstructed manifest: %w", err)
	}
	if err := s.Map.Set(identity, manifestHash, true); err != nil {
		return Hash{}, fmt.Errorf("set map after pull-through: %w", err)
	}
	return manifestHash, nil
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
