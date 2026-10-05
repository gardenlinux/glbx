package log

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type bufTarget struct {
	mu      sync.Mutex
	records []Record
}

func (b *bufTarget) Emit(r Record) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records = append(b.records, r)
}

func TestLevelString(t *testing.T) {
	cases := []struct {
		l    Level
		want string
	}{
		{Debug, "debug"},
		{Info, "info"},
		{Warn, "warn"},
		{Error, "error"},
	}
	for _, c := range cases {
		if got := c.l.String(); got != c.want {
			t.Errorf("Level(%d).String() = %q, want %q", c.l, got, c.want)
		}
	}
}

func TestComponentString(t *testing.T) {
	if Engine.String() != "engine" {
		t.Errorf("Engine.String() = %q", Engine.String())
	}
	if InstallCheck.String() != "install-check" {
		t.Errorf("InstallCheck.String() = %q", InstallCheck.String())
	}
}

func TestLevelJSONRoundTrip(t *testing.T) {
	for _, l := range []Level{Debug, Info, Warn, Error} {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatalf("marshal %v: %v", l, err)
		}
		if !bytes.Contains(b, []byte(`"`)) {
			t.Errorf("level %v emitted as non-string: %s", l, b)
		}
		var got Level
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if got != l {
			t.Errorf("round-trip: got %v want %v (json=%s)", got, l, b)
		}
	}
}

func TestLevelUnmarshalUnknown(t *testing.T) {
	var l Level
	if err := json.Unmarshal([]byte(`"verbose"`), &l); err == nil {
		t.Error("expected error for unknown level")
	}
}

func TestLevelUnmarshalCaseInsensitive(t *testing.T) {
	var l Level
	if err := json.Unmarshal([]byte(`"INFO"`), &l); err != nil {
		t.Fatalf("uppercase INFO should still decode: %v", err)
	}
	if l != Info {
		t.Errorf("got %v, want Info", l)
	}
}

func TestComponentJSONRoundTrip(t *testing.T) {
	all := []Component{Engine, Build, Container, MountNS, UserNS, Deps, Lockfile, Importer, Fetch, Rootfs, Binary, InstallCheck, Chroot, Exec}
	for _, c := range all {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal %v: %v", c, err)
		}
		if !bytes.Contains(b, []byte(`"`)) {
			t.Errorf("component %v emitted as non-string: %s", c, b)
		}
		var got Component
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if got != c {
			t.Errorf("round-trip: got %v want %v (json=%s)", got, c, b)
		}
	}
}

func TestComponentUnmarshalUnknown(t *testing.T) {
	var c Component
	if err := json.Unmarshal([]byte(`"frobnicator"`), &c); err == nil {
		t.Error("expected error for unknown component")
	}
}

func TestLoggerFromContext(t *testing.T) {
	bt := &bufTarget{}
	ctx := WithTarget(context.Background(), bt)
	l := From(ctx, Build)

	l.Info("hello %s", "world")
	l.Debug("detail %d", 42)
	l.Warn("caution")
	l.Error("fail: %v", fmt.Errorf("oops"))

	if len(bt.records) != 4 {
		t.Fatalf("got %d records, want 4", len(bt.records))
	}
	if r := bt.records[0]; r.Level != Info || r.Component != Build || r.Msg != "hello world" {
		t.Errorf("record[0] = %+v", r)
	}
	if r := bt.records[1]; r.Level != Debug || r.Msg != "detail 42" {
		t.Errorf("record[1] = %+v", r)
	}
	if r := bt.records[2]; r.Level != Warn || r.Msg != "caution" {
		t.Errorf("record[2] = %+v", r)
	}
	if r := bt.records[3]; r.Level != Error || r.Msg != "fail: oops" {
		t.Errorf("record[3] = %+v", r)
	}
}

func TestNilTargetDoesNotPanic(t *testing.T) {
	l := From(context.Background(), Engine)
	l.Info("this should not panic")
}

func TestExecWriters(t *testing.T) {
	bt := &bufTarget{}
	ctx := WithTarget(context.Background(), bt)

	stdout, stderr, closeFn := NewExecWriters(ctx, Exec)

	fmt.Fprintln(stdout, "stdout line 1")
	fmt.Fprintln(stdout, "stdout line 2")
	fmt.Fprintln(stderr, "stderr line 1")

	closeFn()

	bt.mu.Lock()
	defer bt.mu.Unlock()

	var infoMsgs, warnMsgs []string
	for _, r := range bt.records {
		if r.Level == Info {
			infoMsgs = append(infoMsgs, r.Msg)
		}
		if r.Level == Warn {
			warnMsgs = append(warnMsgs, r.Msg)
		}
		if r.Component != Exec {
			t.Errorf("unexpected component %v", r.Component)
		}
	}

	if len(infoMsgs) != 2 {
		t.Errorf("got %d info msgs, want 2: %v", len(infoMsgs), infoMsgs)
	}
	if len(warnMsgs) != 1 {
		t.Errorf("got %d warn msgs, want 1: %v", len(warnMsgs), warnMsgs)
	}
	if len(infoMsgs) > 0 && !strings.Contains(infoMsgs[0], "stdout line 1") {
		t.Errorf("info[0] = %q", infoMsgs[0])
	}
	if len(warnMsgs) > 0 && !strings.Contains(warnMsgs[0], "stderr line 1") {
		t.Errorf("warn[0] = %q", warnMsgs[0])
	}
}
