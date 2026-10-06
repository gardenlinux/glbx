package objstore

import (
	"fmt"
	"io"
	"os"
)

// Remote is the read-only backend a pull-through store falls through to on a
// local miss. It is consumed only by the pull-through implementations; a store
// with no remote uses the local implementations directly and never references
// this type.
type Remote interface {
	// OpenBlob fetches a blob by its content hash. Any error (notably one for
	// which errors.Is(err, os.ErrNotExist) holds) is treated as a miss.
	OpenBlob(h Hash) (io.ReadCloser, error)
	// ManifestLeaves returns the leaf outputs an artifact identity resolves to.
	// ok=false means the identity is absent remotely.
	ManifestLeaves(identity Hash) (leaves []Output, ok bool, err error)
}

// nopRemote is a remote that supplies nothing: every blob is a miss and every
// identity is absent. It makes a pull-through store behave exactly like a
// pure-local one, so the pull-through wiring can be composed without a real
// backend.
type nopRemote struct{}

func (nopRemote) OpenBlob(h Hash) (io.ReadCloser, error) {
	return nil, fmt.Errorf("blob %s: %w", h, os.ErrNotExist)
}

func (nopRemote) ManifestLeaves(identity Hash) ([]Output, bool, error) {
	return nil, false, nil
}
