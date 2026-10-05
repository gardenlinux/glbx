package taskui

import (
	"strings"
	"testing"
)

func TestGanttEmptyTracker(t *testing.T) {
	tt := NewTaskTracker()
	out := tt.Gantt()
	// Header should always be present even with no tasks.
	if !strings.Contains(out, "gantt") {
		t.Errorf("expected gantt header, got:\n%s", out)
	}
	if !strings.Contains(out, "title Build Timeline") {
		t.Errorf("expected title line, got:\n%s", out)
	}
}

func TestGanttSkipsPendingTasks(t *testing.T) {
	tt := NewTaskTracker()
	pending := tt.Add("never-started")
	_ = pending // left in Pending state
	finished := tt.Add("finished")
	finished.SetState(InProgress)
	finished.SetState(Success)

	out := tt.Gantt()
	if strings.Contains(out, "never-started") {
		t.Errorf("Gantt should skip Pending tasks (never started), got:\n%s", out)
	}
	if !strings.Contains(out, "finished") {
		t.Errorf("Gantt should include started task, got:\n%s", out)
	}
}

func TestGanttRendersStateMarkers(t *testing.T) {
	tt := NewTaskTracker()
	good := tt.Add("good")
	good.SetState(InProgress)
	good.SetState(Success)

	bad := tt.Add("bad")
	bad.SetState(InProgress)
	bad.SetState(Failed)

	running := tt.Add("running")
	running.SetState(InProgress)

	out := tt.Gantt()
	if !strings.Contains(out, "done,") {
		t.Errorf("expected 'done,' marker for Success task, got:\n%s", out)
	}
	if !strings.Contains(out, "crit,") {
		t.Errorf("expected 'crit,' marker for Failed task, got:\n%s", out)
	}
	if !strings.Contains(out, "active,") {
		t.Errorf("expected 'active,' marker for InProgress task, got:\n%s", out)
	}
}

func TestGanttSortedByStart(t *testing.T) {
	tt := NewTaskTracker()
	// Add in reverse order from the order they "start"; the gantt output
	// should sort them by start time.
	a := tt.Add("alpha")
	a.SetState(InProgress)
	a.SetState(Success)

	b := tt.Add("beta")
	b.SetState(InProgress)
	b.SetState(Success)

	out := tt.Gantt()
	idxA := strings.Index(out, "alpha")
	idxB := strings.Index(out, "beta")
	if idxA < 0 || idxB < 0 {
		t.Fatalf("expected both tasks in output:\n%s", out)
	}
	// alpha was started first, so it should appear before beta.
	if idxA > idxB {
		t.Errorf("alpha (started first) should appear before beta, got:\n%s", out)
	}
}

func TestSanitizeGanttName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"with:colon", "with_colon"},
		{"with#hash", "with_hash"},
		{"both:#issues", "both__issues"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeGanttName(c.in); got != c.want {
			t.Errorf("sanitizeGanttName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGanttSanitizesTaskName(t *testing.T) {
	tt := NewTaskTracker()
	bad := tt.Add("name:with:colons")
	bad.SetState(InProgress)
	bad.SetState(Success)
	out := tt.Gantt()
	// The literal task name with colons would break Mermaid syntax; we
	// expect the output to substitute them with underscores.
	if strings.Contains(out, "name:with:colons :") {
		t.Errorf("colons in task name not sanitized:\n%s", out)
	}
	if !strings.Contains(out, "name_with_colons") {
		t.Errorf("expected sanitized name, got:\n%s", out)
	}
}

func TestGanttNonZeroDuration(t *testing.T) {
	// Even when start == end (synthetic / zero-duration tasks), the gantt
	// emitter must produce endMs > startMs because Mermaid rejects empty
	// ranges. The implementation forces endMs = startMs + 1 in that case.
	tt := NewTaskTracker()
	zero := tt.Add("instant")
	zero.SetState(InProgress)
	zero.SetState(Success)

	out := tt.Gantt()
	// Find the line for "instant"; it should have two distinct millisecond
	// values separated by ", ".
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "instant") {
			if !strings.Contains(line, ", ") {
				t.Errorf("expected start/end pair in line, got: %q", line)
			}
		}
	}
}

func TestTaskTrackerAddAndList(t *testing.T) {
	tt := NewTaskTracker()
	if got := tt.Tasks(); len(got) != 0 {
		t.Errorf("fresh tracker should have 0 tasks, got %d", len(got))
	}
	a := tt.Add("a")
	b := tt.Add("b")
	tasks := tt.Tasks()
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0] != a || tasks[1] != b {
		t.Errorf("tasks not preserved in insertion order")
	}
	// Tasks() returns a copy — mutating it shouldn't affect the tracker.
	tasks[0] = nil
	again := tt.Tasks()
	if again[0] == nil {
		t.Errorf("Tasks() does not return an isolated copy")
	}
}

func TestTaskSetStateRecordsTimestamps(t *testing.T) {
	tt := NewTaskTracker()
	task := tt.Add("timed")
	if !task.Start().IsZero() || !task.End().IsZero() {
		t.Fatalf("fresh task should have zero timestamps")
	}
	task.SetState(InProgress)
	if task.Start().IsZero() {
		t.Errorf("InProgress should set start timestamp")
	}
	if !task.End().IsZero() {
		t.Errorf("End should still be zero while in progress")
	}
	task.SetState(Success)
	if task.End().IsZero() {
		t.Errorf("Success should set end timestamp")
	}
	if !task.End().After(task.Start()) && !task.End().Equal(task.Start()) {
		t.Errorf("end (%v) should be >= start (%v)", task.End(), task.Start())
	}
}

func TestTaskSetStateIdempotentTimestamps(t *testing.T) {
	// SetState should not overwrite an existing start/end timestamp on
	// repeated calls — once a task has started, subsequent state changes
	// shouldn't reset its timeline.
	tt := NewTaskTracker()
	task := tt.Add("x")
	task.SetState(InProgress)
	first := task.Start()
	task.SetState(InProgress) // second In-Progress shouldn't reset start
	if !task.Start().Equal(first) {
		t.Errorf("repeated SetState(InProgress) reset start: %v -> %v", first, task.Start())
	}
	task.SetState(Success)
	end := task.End()
	task.SetState(Failed) // shouldn't overwrite the original end
	if !task.End().Equal(end) {
		t.Errorf("subsequent SetState should not overwrite end: %v -> %v", end, task.End())
	}
}
