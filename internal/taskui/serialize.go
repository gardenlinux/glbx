package taskui

import (
	"encoding/json"
	"io"
	"time"

	"github.com/gardenlinux/glbx/internal/log"
)

type serializedTask struct {
	Name  string            `json:"name"`
	State TaskState         `json:"state"`
	Start time.Time         `json:"start,omitempty"`
	End   time.Time         `json:"end,omitempty"`
	Log   *log.BufferTarget `json:"log"`
}

type serializedTracker struct {
	Tasks []serializedTask `json:"tasks"`
}

// Serialize writes the task tracker state (names, states, logs) to a writer.
func (tt *TaskTracker) Serialize(w io.Writer) error {
	tt.mu.Lock()
	tasks := make([]*Task, len(tt.tasks))
	copy(tasks, tt.tasks)
	tt.mu.Unlock()

	st := serializedTracker{
		Tasks: make([]serializedTask, len(tasks)),
	}
	for i, t := range tasks {
		st.Tasks[i] = serializedTask{
			Name:  t.Name,
			State: t.State(),
			Start: t.Start(),
			End:   t.End(),
			Log:   t.Log,
		}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(st)
}

// Deserialize reads task tracker state from a reader and populates the tracker.
func Deserialize(r io.Reader) (*TaskTracker, error) {
	var st serializedTracker
	if err := json.NewDecoder(r).Decode(&st); err != nil {
		return nil, err
	}

	tt := NewTaskTracker()
	for _, s := range st.Tasks {
		t := &Task{
			Name:  s.Name,
			Log:   s.Log,
			start: s.Start,
			end:   s.End,
		}
		if t.Log == nil {
			t.Log = log.NewBufferTarget()
		}
		t.state = s.State
		tt.tasks = append(tt.tasks, t)
	}
	return tt, nil
}
