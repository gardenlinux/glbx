package taskui

import "testing"

func overviewWithStates(states []TaskState) *TaskOverview {
	tt := NewTaskTracker()
	for i, s := range states {
		task := tt.Add(string(rune('a' + i)))
		task.SetState(s)
	}
	return NewTaskOverview(tt)
}

func TestMoveToNextInProgressJumps(t *testing.T) {
	o := overviewWithStates([]TaskState{Success, InProgress, Pending, InProgress})

	o.MoveToNextInProgress()
	if got := o.Selected(); got != 1 {
		t.Fatalf("first jump: got %d, want 1", got)
	}

	o.MoveToNextInProgress()
	if got := o.Selected(); got != 3 {
		t.Fatalf("second jump from in-progress: got %d, want 3", got)
	}
}

func TestMoveToNextInProgressNoneFollowing(t *testing.T) {
	o := overviewWithStates([]TaskState{Success, InProgress, Success})
	o.MoveToNextInProgress() // selects index 1

	o.MoveToNextInProgress() // nothing in progress after index 1
	if got := o.Selected(); got != 1 {
		t.Fatalf("got %d, want 1 (unchanged)", got)
	}
}

func TestMoveToNextInProgressNoneAtAll(t *testing.T) {
	o := overviewWithStates([]TaskState{Success, Pending, Success})
	o.MoveToNextInProgress()
	if got := o.Selected(); got != 0 {
		t.Fatalf("got %d, want 0 (unchanged)", got)
	}
}
