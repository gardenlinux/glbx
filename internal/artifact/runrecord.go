package artifact

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/taskui"
)

// newRunID returns a random 32-bit id as an 8-character hex string. It names a
// single build invocation locally; a collision only ever overwrites one past
// run record on the same machine, which is a tolerable annoyance, not a
// correctness problem.
func newRunID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// RunNode is one node's entry in a run record: its graph key, the state the
// build left it in, the window it ran in, and the hash of its log blob (zero
// when no log was stored).
type RunNode struct {
	Key   string           `json:"key"`
	State taskui.TaskState `json:"state"`
	Start time.Time        `json:"start,omitempty"`
	End   time.Time        `json:"end,omitempty"`
	Log   objstore.Hash    `json:"log,omitempty"`
}

// RunRecord captures one `glbx build` invocation: a random id, the per-node
// outcomes with pointers to their log blobs, and the engine-log blob. It
// references logs by blob hash, not by map key, so a later rebuild that
// re-points a log map entry leaves a past run's view intact. Run records live
// in the local store only and are never published.
type RunRecord struct {
	ID        string        `json:"id"`
	Nodes     []RunNode     `json:"nodes"`
	EngineLog objstore.Hash `json:"engine_log,omitempty"`
}

// runKey is the map key under which a run record is stored, derived from the
// run id.
func runKey(id string) objstore.Hash {
	return objstore.ConcatHash(id, "build-run")
}

// storeRunRecord serializes a run record to a blob and maps runKey(id) to it.
func storeRunRecord(store *objstore.Store, rec *RunRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("serialize run record: %w", err)
	}
	hash, err := store.Blobs.Store(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("store run record blob: %w", err)
	}
	if err := store.Map.Set(runKey(rec.ID), hash, false); err != nil {
		return fmt.Errorf("map run record: %w", err)
	}
	return nil
}

// LoadRunRecord resolves runKey(id) → blob and decodes the run record. It
// reports a clear miss when no run with that id was recorded.
func LoadRunRecord(store *objstore.Store, id string) (*RunRecord, error) {
	hash, err := store.Map.Get(runKey(id))
	if err != nil {
		return nil, fmt.Errorf("no build run %q: %w", id, err)
	}
	reader, err := store.Blobs.Open(hash)
	if err != nil {
		return nil, fmt.Errorf("open run record %q: %w", id, err)
	}
	defer reader.Close()
	var rec RunRecord
	if err := json.NewDecoder(reader).Decode(&rec); err != nil {
		return nil, fmt.Errorf("decode run record %q: %w", id, err)
	}
	return &rec, nil
}

// TrackerFromRun reconstructs a task tracker from a run record: one task per
// node in its recorded state and run window, with its log loaded from the store
// by hash. It backs both the run-scoped log overview and the run-scoped Gantt
// chart.
func TrackerFromRun(store *objstore.Store, rec *RunRecord) *taskui.TaskTracker {
	tracker := taskui.NewTaskTracker()
	for _, n := range rec.Nodes {
		t := tracker.AddSnapshot(n.Key, n.State, n.Start, n.End)
		if !n.Log.IsZero() {
			_ = loadLogBlobInto(store, n.Log, t.Log)
		}
	}
	return tracker
}
