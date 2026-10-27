package artifact

import (
	"errors"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/log"
)

// After a RunWithUI build the engine's scheduling narrative must live only in
// the per-run engine log, never in a node's own log blob, and each node's log
// must hold what its Build emitted.
func TestEngineLogSeparation(t *testing.T) {
	store := newStore(t)

	a := &mockArtifact{name: "A", identity: strings.Repeat("a", 64), buildFn: func(bctx BuildContext) ([]Output, error) {
		log.From(bctx.Ctx, log.Build).Info("artifact A doing work")
		return nil, nil
	}}

	g := NewGraph()
	g.Add(a)

	engine := NewEngine(g, store, 1)
	results, err := engine.RunWithUI(testCtx())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("unexpected results: %+v", results)
	}

	id, err := a.Identity()
	if err != nil {
		t.Fatal(err)
	}

	// The node log holds the artifact's own output and none of the engine's
	// scheduling lines.
	nodeLog := log.NewBufferTarget()
	if err := loadLogInto(store, id, nodeLog); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(bufMsgs(nodeLog), "\n")
	if !strings.Contains(joined, "artifact A doing work") {
		t.Fatalf("node log missing the artifact's own output: %q", joined)
	}
	for _, chatter := range []string{"building:", "complete:", "cache hit:"} {
		if strings.Contains(joined, chatter) {
			t.Fatalf("node log leaked engine chatter %q: %q", chatter, joined)
		}
	}

	// The engine buffer carries the scheduling narrative.
	if engineMsgs := bufMsgs(engine.engineLog); !containsAny(engineMsgs, "building: A") {
		t.Fatalf("engine log missing scheduling narrative: %v", engineMsgs)
	}
}

// A failed node's FAILED line lands in both its node log (the failure detail)
// and the engine log (the source the exit flush prints from).
func TestEngineLogFailureInBothBuffers(t *testing.T) {
	store := newStore(t)

	a := &mockArtifact{name: "A", identity: strings.Repeat("a", 64), buildFn: func(bctx BuildContext) ([]Output, error) {
		return nil, errBoom
	}}
	g := NewGraph()
	g.Add(a)

	engine := NewEngine(g, store, 1)
	if _, err := engine.RunWithUI(testCtx()); err != nil {
		t.Fatal(err)
	}

	id, _ := a.Identity()
	nodeLog := log.NewBufferTarget()
	if err := loadLogInto(store, id, nodeLog); err != nil {
		t.Fatal(err)
	}
	if !containsAny(bufMsgs(nodeLog), "FAILED: A") {
		t.Fatal("node log missing FAILED detail")
	}
	if !errorRecorded(engine.engineLog) {
		t.Fatal("engine log missing the ERROR copy the exit flush needs")
	}
}

func containsAny(msgs []string, substr string) bool {
	for _, m := range msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

func errorRecorded(buf *log.BufferTarget) bool {
	r := buf.Reader()
	for {
		rec, err := r.Read()
		if err == log.ErrNoMore {
			return false
		}
		if rec.Level == log.Error {
			return true
		}
	}
}

var errBoom = errors.New("boom")
