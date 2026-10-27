package artifact

import (
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

func newStore(t *testing.T) *objstore.Store {
	t.Helper()
	store, err := objstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func bufWith(msgs ...string) *log.BufferTarget {
	buf := log.NewBufferTarget()
	for _, m := range msgs {
		buf.Emit(log.Record{Level: log.Info, Component: log.Build, Msg: m})
	}
	return buf
}

func bufMsgs(buf *log.BufferTarget) []string {
	var out []string
	r := buf.Reader()
	for {
		rec, err := r.Read()
		if err == log.ErrNoMore {
			break
		}
		out = append(out, rec.Msg)
	}
	return out
}

func TestLogKeyDeterministicAndDistinct(t *testing.T) {
	id := objstore.MustHash(strings.Repeat("a", 64))
	if logKey(id) != logKey(id) {
		t.Fatal("logKey not deterministic")
	}
	if logKey(id).Equal(id) {
		t.Fatal("logKey collides with the identity it derives from")
	}
	other := objstore.MustHash(strings.Repeat("b", 64))
	if logKey(id).Equal(logKey(other)) {
		t.Fatal("logKey collides across identities")
	}
	if logKey(id).Equal(runKey(id.String())) {
		t.Fatal("logKey collides with runKey")
	}
}

func TestStoreAndLoadLog(t *testing.T) {
	store := newStore(t)
	id := objstore.MustHash(strings.Repeat("c", 64))

	if _, err := storeLog(store, id, bufWith("first line", "second line")); err != nil {
		t.Fatal(err)
	}

	got := log.NewBufferTarget()
	if err := loadLogInto(store, id, got); err != nil {
		t.Fatal(err)
	}
	msgs := bufMsgs(got)
	if len(msgs) != 2 || msgs[0] != "first line" || msgs[1] != "second line" {
		t.Fatalf("round-trip mismatch: %v", msgs)
	}
}

func TestLoadLogMissingIsError(t *testing.T) {
	store := newStore(t)
	id := objstore.MustHash(strings.Repeat("d", 64))
	if err := loadLogInto(store, id, log.NewBufferTarget()); err == nil {
		t.Fatal("expected a miss for an identity with no stored log")
	}
}

// A rebuild that overwrites a node's log map entry must not disturb the blob a
// past run record points at: the old failure log stays resolvable by hash.
func TestOverwriteLogKeepsOldBlob(t *testing.T) {
	store := newStore(t)
	id := objstore.MustHash(strings.Repeat("e", 64))

	oldHash, err := storeLog(store, id, bufWith("old failure"))
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := storeLog(store, id, bufWith("new success"))
	if err != nil {
		t.Fatal(err)
	}
	if oldHash.Equal(newHash) {
		t.Fatal("different contents must produce different blob hashes")
	}

	// The map now points at the new blob.
	viaMap := log.NewBufferTarget()
	if err := loadLogInto(store, id, viaMap); err != nil {
		t.Fatal(err)
	}
	if msgs := bufMsgs(viaMap); len(msgs) != 1 || msgs[0] != "new success" {
		t.Fatalf("map should resolve to the new log: %v", msgs)
	}

	// The old blob is still readable by hash.
	viaBlob := log.NewBufferTarget()
	if err := loadLogBlobInto(store, oldHash, viaBlob); err != nil {
		t.Fatal(err)
	}
	if msgs := bufMsgs(viaBlob); len(msgs) != 1 || msgs[0] != "old failure" {
		t.Fatalf("old blob should still resolve to the old log: %v", msgs)
	}
}

func TestRunRecordRoundTrip(t *testing.T) {
	store := newStore(t)
	id := objstore.MustHash(strings.Repeat("f", 64))

	logHash, err := storeLog(store, id, bufWith("the build log"))
	if err != nil {
		t.Fatal(err)
	}
	engineHash, err := storeLogBlob(store, bufWith("building: X", "complete: X"))
	if err != nil {
		t.Fatal(err)
	}

	rec := &RunRecord{
		ID:        "deadbeef",
		EngineLog: engineHash,
		Nodes:     []RunNode{{Key: "X", Log: logHash}},
	}
	if err := storeRunRecord(store, rec); err != nil {
		t.Fatal(err)
	}

	// Overwrite X's log map entry, as a later successful rebuild would.
	if _, err := storeLog(store, id, bufWith("a newer log")); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadRunRecord(store, "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Nodes) != 1 || loaded.Nodes[0].Key != "X" {
		t.Fatalf("unexpected nodes: %+v", loaded.Nodes)
	}

	// The run record still resolves the original log, not the overwritten one.
	got := log.NewBufferTarget()
	if err := loadLogBlobInto(store, loaded.Nodes[0].Log, got); err != nil {
		t.Fatal(err)
	}
	if msgs := bufMsgs(got); len(msgs) != 1 || msgs[0] != "the build log" {
		t.Fatalf("run record should pin the original log: %v", msgs)
	}
}

func TestLoadRunRecordMissing(t *testing.T) {
	store := newStore(t)
	if _, err := LoadRunRecord(store, "00000000"); err == nil {
		t.Fatal("expected an error for an unknown run id")
	}
}
