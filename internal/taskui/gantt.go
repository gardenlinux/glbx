package taskui

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type ganttEntry struct {
	name       string
	start, end time.Time
	state      TaskState
}

// Gantt renders the task tracker as a Mermaid gantt chart, showing
// when each task ran and how long it took. Pending tasks (never started)
// are omitted. The chart uses the earliest task start time as t=0.
func (tt *TaskTracker) Gantt() string {
	tasks := tt.Tasks()

	var entries []ganttEntry
	var origin time.Time
	for _, t := range tasks {
		s, e := t.Start(), t.End()
		if s.IsZero() {
			continue
		}
		if origin.IsZero() || s.Before(origin) {
			origin = s
		}
		entries = append(entries, ganttEntry{name: t.Name, start: s, end: e, state: t.State()})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].start.Before(entries[j].start)
	})

	var latest time.Time
	for _, e := range entries {
		if e.end.After(latest) {
			latest = e.end
		}
	}

	var sb strings.Builder
	sb.WriteString("%%{init: {'themeVariables': {")
	sb.WriteString("'sectionBkgColor':'#424242','altSectionBkgColor':'#525252',")
	sb.WriteString("'gridColor':'#757575',")
	sb.WriteString("'doneTaskBkgColor':'#4caf50','doneTaskBorderColor':'#2e7d32',")
	sb.WriteString("'critBkgColor':'#e53935','critBorderColor':'#b71c1c',")
	sb.WriteString("'activeTaskBkgColor':'#9e9e9e','activeTaskBorderColor':'#616161',")
	sb.WriteString("'taskBkgColor':'#9e9e9e','taskBorderColor':'#616161'")
	sb.WriteString("}}}%%\n")
	sb.WriteString("gantt\n")
	sb.WriteString("    title Build Timeline\n")
	sb.WriteString("    dateFormat x\n")
	sb.WriteString("    axisFormat %M:%S\n")

	for _, e := range entries {
		end := e.end
		if end.IsZero() {
			end = latest
			if end.Before(e.start) {
				end = e.start
			}
		}
		startMs := e.start.Sub(origin).Milliseconds()
		endMs := end.Sub(origin).Milliseconds()
		if endMs <= startMs {
			endMs = startMs + 1
		}
		var status string
		switch e.state {
		case Failed:
			status = "crit, "
		case InProgress:
			status = "active, "
		case Success:
			status = "done, "
		}
		sb.WriteString(fmt.Sprintf("    %s :%s%d, %d\n", sanitizeGanttName(e.name), status, startMs, endMs))
	}

	return sb.String()
}

// sanitizeGanttName escapes characters that would break a Mermaid gantt task line.
// Mermaid uses ':' to split name from the spec, so we replace it.
func sanitizeGanttName(s string) string {
	r := strings.NewReplacer(":", "_", "#", "_")
	return r.Replace(s)
}
