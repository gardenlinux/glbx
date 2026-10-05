package taskui

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gardenlinux/glbx/internal/log"
)

type TaskState int

const (
	Pending TaskState = iota
	InProgress
	Success
	Failed
)

func (s TaskState) String() string {
	switch s {
	case Pending:
		return "pending"
	case InProgress:
		return "in_progress"
	case Success:
		return "success"
	case Failed:
		return "failed"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

func (s TaskState) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

func (s *TaskState) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch raw {
	case "pending":
		*s = Pending
	case "in_progress":
		*s = InProgress
	case "success":
		*s = Success
	case "failed":
		*s = Failed
	default:
		return fmt.Errorf("taskui: unknown state %q", raw)
	}
	return nil
}

type Task struct {
	Name  string
	state TaskState
	start time.Time
	end   time.Time
	mu    sync.Mutex
	Log   *log.BufferTarget
}

func (t *Task) State() TaskState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

func (t *Task) SetState(s TaskState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = s
	switch s {
	case InProgress:
		if t.start.IsZero() {
			t.start = time.Now()
		}
	case Success, Failed:
		if t.end.IsZero() {
			t.end = time.Now()
		}
	}
}

func (t *Task) Start() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.start
}

func (t *Task) End() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.end
}

type TaskTracker struct {
	mu    sync.Mutex
	tasks []*Task
}

func NewTaskTracker() *TaskTracker {
	return &TaskTracker{}
}

func (tt *TaskTracker) Add(name string) *Task {
	t := &Task{
		Name:  name,
		state: Pending,
		Log:   log.NewBufferTarget(),
	}
	tt.mu.Lock()
	tt.tasks = append(tt.tasks, t)
	tt.mu.Unlock()
	return t
}

func (tt *TaskTracker) Tasks() []*Task {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	out := make([]*Task, len(tt.tasks))
	copy(out, tt.tasks)
	return out
}
