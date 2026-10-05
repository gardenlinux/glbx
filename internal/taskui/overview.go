package taskui

import (
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	circleEmpty  = "○"
	circleFilled = "●"
	arrowUp      = "▲"
	arrowDown    = "▼"

	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiCyan   = "\033[36m"
	ansiCurUp  = "\033[%dA"
	ansiCurCol = "\033[0G"
	ansiClear  = "\033[2K"
)

type TaskOverview struct {
	tracker   *TaskTracker
	mu        sync.Mutex
	shown     bool
	done      chan struct{}
	linesLast int
	blinkOn   bool
	selected  int
	inputDone chan struct{}
	redraw    chan struct{}
	quit      chan struct{}

	// OnEnter is called (in the input goroutine) when Enter is pressed.
	// The stop channel is closed when the overview is shutting down —
	// the callback must return promptly when this happens.
	OnEnter func(task *Task, stop <-chan struct{})
}

func NewTaskOverview(tracker *TaskTracker) *TaskOverview {
	return &TaskOverview{
		tracker:   tracker,
		done:      make(chan struct{}),
		inputDone: make(chan struct{}),
		redraw:    make(chan struct{}, 1),
		quit:      make(chan struct{}),
	}
}

func (o *TaskOverview) Show() {
	o.mu.Lock()
	if o.shown {
		o.mu.Unlock()
		return
	}
	o.shown = true
	o.done = make(chan struct{})
	o.inputDone = make(chan struct{})
	o.mu.Unlock()

	go o.drawLoop()
	go o.inputLoop()
}

// Hide stops the overview and clears its drawn output.
// If the user is currently in a log view (OnEnter), it signals
// the callback to return and waits for it.
func (o *TaskOverview) Hide() {
	o.mu.Lock()
	wasShown := o.shown
	o.shown = false
	o.mu.Unlock()

	// Signal quit to interrupt OnEnter if it's running
	select {
	case <-o.quit:
		// already closed
	default:
		close(o.quit)
	}

	if wasShown {
		<-o.done
	}
	<-o.inputDone
	o.clearDrawn()
}

func (o *TaskOverview) MoveUp() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.selected > 0 {
		o.selected--
	}
}

func (o *TaskOverview) MoveDown() {
	o.mu.Lock()
	defer o.mu.Unlock()
	total := len(o.tracker.Tasks())
	if o.selected < total-1 {
		o.selected++
	}
}

func (o *TaskOverview) Selected() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.selected
}

func (o *TaskOverview) triggerRedraw() {
	select {
	case o.redraw <- struct{}{}:
	default:
	}
}

func (o *TaskOverview) clearDrawn() {
	if o.linesLast > 0 {
		fmt.Fprintf(os.Stderr, ansiCurUp+ansiCurCol, o.linesLast)
		for range o.linesLast {
			fmt.Fprintf(os.Stderr, ansiClear+"\n")
		}
		fmt.Fprintf(os.Stderr, ansiCurUp+ansiCurCol, o.linesLast)
		o.linesLast = 0
	}
}

func (o *TaskOverview) isQuitting() bool {
	select {
	case <-o.quit:
		return true
	default:
		return false
	}
}

func (o *TaskOverview) inputLoop() {
	defer close(o.inputDone)

	fd := int(os.Stdin.Fd())

	oldTermios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return
	}

	newTermios := *oldTermios
	newTermios.Lflag &^= unix.ICANON | unix.ECHO
	newTermios.Cc[unix.VMIN] = 1
	newTermios.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &newTermios); err != nil {
		return
	}
	defer unix.IoctlSetTermios(fd, unix.TCSETS, oldTermios)

	pollFds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	buf := make([]byte, 3)

	for {
		if o.isQuitting() {
			return
		}

		o.mu.Lock()
		if !o.shown {
			o.mu.Unlock()
			if o.isQuitting() {
				return
			}
			// Not shown but not quitting — shouldn't happen in normal flow,
			// but just in case, poll briefly and recheck.
			unix.Poll(pollFds, 200)
			continue
		}
		o.mu.Unlock()

		n, err := unix.Poll(pollFds, 200)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}

		nRead, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}

		if o.isQuitting() {
			return
		}

		o.mu.Lock()
		if !o.shown {
			o.mu.Unlock()
			continue
		}
		o.mu.Unlock()

		if nRead == 3 && buf[0] == 0x1b && buf[1] == '[' {
			switch buf[2] {
			case 'A':
				o.MoveUp()
				o.triggerRedraw()
			case 'B':
				o.MoveDown()
				o.triggerRedraw()
			}
		} else if nRead == 1 && (buf[0] == '\n' || buf[0] == '\r') {
			if o.OnEnter != nil {
				tasks := o.tracker.Tasks()
				o.mu.Lock()
				sel := o.selected
				o.mu.Unlock()
				if sel < len(tasks) {
					o.mu.Lock()
					o.shown = false
					o.mu.Unlock()
					<-o.done
					o.clearDrawn()

					o.OnEnter(tasks[sel], o.quit)

					if o.isQuitting() {
						return
					}

					o.mu.Lock()
					o.shown = true
					o.linesLast = 0
					o.done = make(chan struct{})
					o.mu.Unlock()
					go o.drawLoop()
				}
			}
		}
	}
}

func (o *TaskOverview) drawLoop() {
	defer close(o.done)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	o.draw()

	for {
		select {
		case <-ticker.C:
			o.blinkOn = !o.blinkOn
		case <-o.redraw:
		}

		o.mu.Lock()
		if !o.shown {
			o.mu.Unlock()
			return
		}
		o.mu.Unlock()
		o.draw()
	}
}

func (o *TaskOverview) draw() {
	tasks := o.tracker.Tasks()
	total := len(tasks)
	if total == 0 {
		return
	}

	_, height, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil {
		height = 24
	}
	height--

	o.mu.Lock()
	sel := o.selected
	o.mu.Unlock()

	if sel >= total {
		sel = total - 1
	}

	type line struct {
		text string
	}

	var lines []line

	if total <= height {
		for i, t := range tasks {
			lines = append(lines, line{text: o.formatTask(t, i == sel)})
		}
	} else {
		available := height
		selLine := 1

		remaining := available - selLine
		aboveWant := remaining / 2
		belowWant := remaining - aboveWant

		aboveHave := sel
		belowHave := total - sel - 1

		aboveShow := aboveWant
		belowShow := belowWant

		if aboveHave < aboveShow {
			belowShow += aboveShow - aboveHave
			aboveShow = aboveHave
		} else if belowHave < belowShow {
			aboveShow += belowShow - belowHave
			belowShow = belowHave
		}

		topArrow := aboveHave > aboveShow
		bottomArrow := belowHave > belowShow

		if topArrow {
			aboveShow--
		}
		if bottomArrow {
			belowShow--
		}

		if topArrow {
			lines = append(lines, line{text: fmt.Sprintf(" %s (%d more)", arrowUp, aboveHave-aboveShow)})
		}

		startIdx := sel - aboveShow
		for i := startIdx; i < sel; i++ {
			lines = append(lines, line{text: o.formatTask(tasks[i], false)})
		}

		lines = append(lines, line{text: o.formatTask(tasks[sel], true)})

		for i := sel + 1; i <= sel+belowShow; i++ {
			lines = append(lines, line{text: o.formatTask(tasks[i], false)})
		}

		if bottomArrow {
			lines = append(lines, line{text: fmt.Sprintf(" %s (%d more)", arrowDown, belowHave-belowShow)})
		}
	}

	if o.linesLast > 0 {
		fmt.Fprintf(os.Stderr, ansiCurUp+ansiCurCol, o.linesLast)
	}

	for _, l := range lines {
		fmt.Fprintf(os.Stderr, ansiClear+"%s\n", l.text)
	}

	for i := len(lines); i < o.linesLast; i++ {
		fmt.Fprintf(os.Stderr, ansiClear+"\n")
	}

	if len(lines) < o.linesLast {
		extra := o.linesLast - len(lines)
		fmt.Fprintf(os.Stderr, ansiCurUp+ansiCurCol, extra)
	}

	o.linesLast = len(lines)
}

func (o *TaskOverview) formatTask(t *Task, selected bool) string {
	state := t.State()
	icon := o.icon(state)
	name := t.Name
	if selected {
		name = ansiCyan + name + ansiReset
	}
	return fmt.Sprintf(" %s %s", icon, name)
}

func (o *TaskOverview) icon(s TaskState) string {
	switch s {
	case Pending:
		return circleEmpty
	case InProgress:
		if o.blinkOn {
			return circleFilled
		}
		return circleEmpty
	case Success:
		return ansiGreen + circleFilled + ansiReset
	case Failed:
		return ansiRed + circleFilled + ansiReset
	}
	return "?"
}

// PrintPlain prints all tasks once with their current state to stderr.
// No dynamic updates, no cursor movement, no selection highlighting.
func (tt *TaskTracker) PrintPlain() {
	for _, t := range tt.Tasks() {
		state := t.State()
		var icon string
		switch state {
		case Pending:
			icon = circleEmpty
		case InProgress:
			icon = circleFilled
		case Success:
			icon = ansiGreen + circleFilled + ansiReset
		case Failed:
			icon = ansiRed + circleFilled + ansiReset
		}
		fmt.Fprintf(os.Stderr, " %s %s\n", icon, t.Name)
	}
}

// NonInteractiveViewer prints a status summary line every second when
// the task state counts change. Call Stop() to end it.
type NonInteractiveViewer struct {
	tracker *TaskTracker
	done    chan struct{}
	stopped chan struct{}
}

func NewNonInteractiveViewer(tracker *TaskTracker) *NonInteractiveViewer {
	return &NonInteractiveViewer{
		tracker: tracker,
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (v *NonInteractiveViewer) Start() {
	go v.loop()
}

func (v *NonInteractiveViewer) Stop() {
	close(v.done)
	<-v.stopped
}

func (v *NonInteractiveViewer) loop() {
	defer close(v.stopped)

	var lastPending, lastInProgress, lastSuccess, lastFailed int
	first := true

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-v.done:
			return
		case <-ticker.C:
			tasks := v.tracker.Tasks()
			var pending, inProgress, success, failed int
			for _, t := range tasks {
				switch t.State() {
				case Pending:
					pending++
				case InProgress:
					inProgress++
				case Success:
					success++
				case Failed:
					failed++
				}
			}
			if first || pending != lastPending || inProgress != lastInProgress || success != lastSuccess || failed != lastFailed {
				fmt.Fprintf(os.Stdout, "tasks: %d pending, %d in progress, %d success, %d failed\n",
					pending, inProgress, success, failed)
				lastPending = pending
				lastInProgress = inProgress
				lastSuccess = success
				lastFailed = failed
				first = false
			}
		}
	}
}
