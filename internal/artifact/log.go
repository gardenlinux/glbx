package artifact

import (
	"bytes"
	"fmt"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// logKey is the map key under which a node's build-log blob is recorded. It is
// derived from the artifact identity so the log for a built node is reachable
// from the identity alone, the same way its output manifest is. The identity
// already carries the scheme seeds, so the key needs no version of its own.
func logKey(identity objstore.Hash) objstore.Hash {
	return objstore.ConcatHash(identity.String(), "logs")
}

// storeLog serializes a node's log buffer to a blob and maps logKey(identity) to
// it, returning the blob hash. The entry is written for every attempted build,
// success or failure; its presence never influences the rebuild decision.
func storeLog(store *objstore.Store, identity objstore.Hash, buf *log.BufferTarget) (objstore.Hash, error) {
	hash, err := storeLogBlob(store, buf)
	if err != nil {
		return objstore.Hash{}, err
	}
	if err := store.Map.Set(logKey(identity), hash, false); err != nil {
		return objstore.Hash{}, fmt.Errorf("map log entry: %w", err)
	}
	return hash, nil
}

// storeLogBlob serializes a log buffer to a content-addressed blob and returns
// its hash, without recording any map entry. The engine log, which no map entry
// points at, is stored this way.
func storeLogBlob(store *objstore.Store, buf *log.BufferTarget) (objstore.Hash, error) {
	data, err := buf.MarshalJSON()
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("serialize log: %w", err)
	}
	hash, err := store.Blobs.Store(bytes.NewReader(data))
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("store log blob: %w", err)
	}
	return hash, nil
}

// loadLogInto resolves logKey(identity) → blob and reads the stored records into
// dst. A missing map entry means no log was ever recorded for this identity and
// is returned as is: callers treat it as "no log", not a hard failure.
func loadLogInto(store *objstore.Store, identity objstore.Hash, dst *log.BufferTarget) error {
	hash, err := store.Map.Get(logKey(identity))
	if err != nil {
		return err
	}
	return loadLogBlobInto(store, hash, dst)
}

// loadLogBlobInto reads a log blob by hash into dst, bypassing the map. The
// run-record path uses this: it holds blob hashes directly so a later rebuild
// that re-points the log map entry does not change what a past run resolves.
func loadLogBlobInto(store *objstore.Store, hash objstore.Hash, dst *log.BufferTarget) error {
	reader, err := store.Blobs.Open(hash)
	if err != nil {
		return err
	}
	defer reader.Close()
	_, err = dst.ReadFrom(reader)
	return err
}

// LoadNodeLog reads the stored build log for an artifact into dst, resolved from
// the artifact's identity. A missing entry (never built, or built before logs
// were recorded) returns an error the caller can treat as "no log".
func LoadNodeLog(store *objstore.Store, a Artifact, dst *log.BufferTarget) error {
	identity, err := a.Identity()
	if err != nil {
		return err
	}
	return loadLogInto(store, identity, dst)
}

// LoadLogBlob reads a log blob by hash into dst. It is the by-hash form used
// when a run record names the log directly.
func LoadLogBlob(store *objstore.Store, hash objstore.Hash, dst *log.BufferTarget) error {
	return loadLogBlobInto(store, hash, dst)
}

// LogAlias returns the published map alias for an artifact's log — its log-key
// and the log blob the key resolves to — when the store holds one. A node with
// no recorded log reports ok=false. Publishing uses this to mirror a node's log
// alongside its outputs: it is only ever consulted for a node whose manifest is
// already being published, so a log is never published without its outputs.
func LogAlias(store *objstore.Store, a Artifact) (alias objstore.MapAlias, ok bool) {
	identity, err := a.Identity()
	if err != nil {
		return objstore.MapAlias{}, false
	}
	key := logKey(identity)
	blob, err := store.Map.Get(key)
	if err != nil {
		return objstore.MapAlias{}, false
	}
	return objstore.MapAlias{Key: key, Value: blob}, true
}
