package log

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBufferTargetMarshalJSON(t *testing.T) {
	bt := NewBufferTarget()
	bt.Emit(Record{Level: Info, Component: Build, Msg: "hello"})
	bt.Emit(Record{Level: Error, Component: Importer, Msg: "boom"})

	data, err := bt.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	// The wire format uses the string forms of level and component, so logs
	// stay readable across iota renumbering.
	s := string(data)
	for _, want := range []string{`"level":"info"`, `"level":"error"`, `"component":"build"`, `"component":"importer"`, `"msg":"hello"`, `"msg":"boom"`} {
		if !bytes.Contains([]byte(s), []byte(want)) {
			t.Errorf("marshalled JSON missing %q: %s", want, s)
		}
	}
}

// An empty buffer must marshal to a JSON array, not null, so consumers can
// range over the result without a nil check.
func TestBufferTargetMarshalEmpty(t *testing.T) {
	bt := NewBufferTarget()
	data, err := bt.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if got := string(data); got != "[]" {
		t.Errorf("empty buffer marshalled as %q, want %q", got, "[]")
	}
}

// Unmarshalling into a zero-value BufferTarget re-creates the cond, so the next
// Emit does not panic on a nil cond.
func TestBufferTargetRoundTrip(t *testing.T) {
	src := NewBufferTarget()
	src.Emit(Record{Level: Info, Component: Build, Msg: "first"})
	src.Emit(Record{Level: Warn, Component: Importer, Msg: "second"})
	src.Emit(Record{Level: Debug, Component: Container, Msg: "third"})

	data, err := src.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var dst BufferTarget // zero value: cond is nil until UnmarshalJSON
	if err := dst.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if dst.Len() != 3 {
		t.Fatalf("dst.Len() = %d, want 3", dst.Len())
	}

	r := dst.Reader()
	want := []Record{
		{Level: Info, Component: Build, Msg: "first"},
		{Level: Warn, Component: Importer, Msg: "second"},
		{Level: Debug, Component: Container, Msg: "third"},
	}
	for i, w := range want {
		got, err := r.Read()
		if err != nil {
			t.Fatalf("Read[%d]: %v", i, err)
		}
		if got != w {
			t.Errorf("rec[%d] = %+v, want %+v", i, got, w)
		}
	}

	dst.Emit(Record{Level: Error, Component: Engine, Msg: "post-unmarshal"})
	if dst.Len() != 4 {
		t.Errorf("dst.Len() after post-unmarshal Emit = %d, want 4", dst.Len())
	}
}

// UnmarshalJSON replaces existing records rather than appending.
func TestBufferTargetUnmarshalReplacesContent(t *testing.T) {
	bt := NewBufferTarget()
	bt.Emit(Record{Level: Info, Component: Build, Msg: "old"})

	payload, err := json.Marshal([]map[string]any{
		{"level": "warn", "component": "importer", "msg": "new"},
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if err := bt.UnmarshalJSON(payload); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if bt.Len() != 1 {
		t.Fatalf("Len after Unmarshal = %d, want 1", bt.Len())
	}
	r := bt.Reader()
	rec, err := r.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Msg != "new" || rec.Level != Warn || rec.Component != Importer {
		t.Errorf("rec = %+v, want Warn/Importer/new", rec)
	}
}

func TestBufferTargetUnmarshalInvalidJSON(t *testing.T) {
	var bt BufferTarget
	if err := bt.UnmarshalJSON([]byte("not json")); err == nil {
		t.Fatal("expected error from UnmarshalJSON on garbage input, got nil")
	}
}

func TestBufferTargetUnmarshalUnknownLevel(t *testing.T) {
	var bt BufferTarget
	payload := []byte(`[{"level":"trace","component":"build","msg":"x"}]`)
	if err := bt.UnmarshalJSON(payload); err == nil {
		t.Fatal("expected error for unknown level 'trace', got nil")
	}
}

func TestBufferTargetWriteToReadFrom(t *testing.T) {
	src := NewBufferTarget()
	src.Emit(Record{Level: Info, Component: Build, Msg: "alpha"})
	src.Emit(Record{Level: Debug, Component: Importer, Msg: "beta"})

	var buf bytes.Buffer
	n, err := src.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if n == 0 || buf.Len() == 0 {
		t.Fatal("WriteTo produced no bytes")
	}

	dst := NewBufferTarget()
	if _, err := dst.ReadFrom(&buf); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if dst.Len() != 2 {
		t.Fatalf("dst.Len() = %d, want 2", dst.Len())
	}
	r := dst.Reader()
	if rec, _ := r.Read(); rec.Msg != "alpha" {
		t.Errorf("first rec msg = %q, want %q", rec.Msg, "alpha")
	}
	if rec, _ := r.Read(); rec.Msg != "beta" {
		t.Errorf("second rec msg = %q, want %q", rec.Msg, "beta")
	}
}
