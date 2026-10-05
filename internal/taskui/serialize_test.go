package taskui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskStateString(t *testing.T) {
	cases := []struct {
		s    TaskState
		want string
	}{
		{Pending, "pending"},
		{InProgress, "in_progress"},
		{Success, "success"},
		{Failed, "failed"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("TaskState(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestTaskStateJSONRoundTrip(t *testing.T) {
	for _, s := range []TaskState{Pending, InProgress, Success, Failed} {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal %v: %v", s, err)
		}
		if !bytes.Contains(b, []byte(`"`)) {
			t.Errorf("state %v emitted as non-string: %s", s, b)
		}
		var got TaskState
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if got != s {
			t.Errorf("round-trip: got %v want %v (json=%s)", got, s, b)
		}
	}
}

func TestTaskStateUnmarshalUnknown(t *testing.T) {
	var s TaskState
	if err := json.Unmarshal([]byte(`"halfway"`), &s); err == nil {
		t.Error("expected error for unknown state")
	}
}

func TestTrackerSerializeProducesStrings(t *testing.T) {
	tt := NewTaskTracker()
	a := tt.Add("alpha")
	a.SetState(Success)
	b := tt.Add("beta")
	b.SetState(Failed)

	var buf bytes.Buffer
	if err := tt.Serialize(&buf); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"state": "success"`) {
		t.Errorf("expected state success as string in output, got:\n%s", out)
	}
	if !strings.Contains(out, `"state": "failed"`) {
		t.Errorf("expected state failed as string in output, got:\n%s", out)
	}

	tt2, err := Deserialize(strings.NewReader(out))
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	tasks := tt2.Tasks()
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].State() != Success {
		t.Errorf("alpha state: got %v want Success", tasks[0].State())
	}
	if tasks[1].State() != Failed {
		t.Errorf("beta state: got %v want Failed", tasks[1].State())
	}
}
