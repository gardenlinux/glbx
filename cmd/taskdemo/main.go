package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/taskui"
	"golang.org/x/term"
)

func main() {
	// --view-logs <PATH> mode
	if len(os.Args) >= 3 && os.Args[1] == "--view-logs" {
		viewLogs(os.Args[2])
		return
	}

	fillers := 0
	if len(os.Args) > 1 {
		n, err := strconv.Atoi(os.Args[1])
		if err == nil && n > 0 {
			fillers = n
		}
	}

	tracker := taskui.NewTaskTracker()

	var tasks []*taskui.Task
	for i := 1; i <= 10; i++ {
		tasks = append(tasks, tracker.Add(fmt.Sprintf("task-%d", i)))
	}

	for i := 1; i <= fillers; i++ {
		tracker.Add(fmt.Sprintf("filler-%d", i))
	}

	endless := tracker.Add("endless-task")
	endless.SetState(taskui.InProgress)

	interactive := term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))

	var overview *taskui.TaskOverview
	var viewer *taskui.NonInteractiveViewer

	if interactive {
		overview = taskui.NewTaskOverview(tracker)
		overview.OnEnter = func(task *taskui.Task, stop <-chan struct{}) {
			fmt.Fprintf(os.Stderr, "--- logs: %s (press q to return) ---\n", task.Name)

			printer := log.NewLogPrinter(task.Log)
			printer.Run()

			done := make(chan struct{})
			go func() {
				buf := make([]byte, 1)
				for {
					n, err := os.Stdin.Read(buf)
					if err != nil {
						break
					}
					if n == 1 && buf[0] == 'q' {
						break
					}
				}
				close(done)
			}()

			select {
			case <-done:
			case <-stop:
			}

			printer.Stop()
			fmt.Fprintf(os.Stderr, "--- end logs ---\n")
		}
		overview.Show()
	} else {
		viewer = taskui.NewNonInteractiveViewer(tracker)
		viewer.Start()
	}

	time.Sleep(1 * time.Second)

	for i, t := range tasks {
		logCtx := log.WithTarget(context.Background(), t.Log)
		logger := log.From(logCtx, log.Build)

		t.SetState(taskui.InProgress)
		logger.Info("started")

		time.Sleep(2 * time.Second)

		if (i+1)%3 == 0 {
			logger.Error("done (failure)")
			t.SetState(taskui.Failed)
		} else {
			logger.Info("done (success)")
			t.SetState(taskui.Success)
		}
	}

	time.Sleep(1 * time.Second)

	if interactive {
		overview.Hide()
	} else {
		viewer.Stop()
	}

	fmt.Fprintf(os.Stderr, "\n")
	tracker.PrintPlain()

	// Serialize task tracker to temp file
	f, err := os.CreateTemp("", "taskdemo-*.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp file: %v\n", err)
		os.Exit(1)
	}
	if err := tracker.Serialize(f); err != nil {
		fmt.Fprintf(os.Stderr, "failed to serialize: %v\n", err)
		os.Exit(1)
	}
	f.Close()

	total := len(tracker.Tasks())
	fmt.Fprintf(os.Stderr, "\nCompleted %d tasks. Logs written to: %s. Investigate logs with:\n\n%s --view-logs %s\n", total, f.Name(), os.Args[0], f.Name())
}

func viewLogs(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open %s: %v\n", path, err)
		os.Exit(1)
	}
	defer f.Close()

	tracker, err := taskui.Deserialize(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to deserialize %s: %v\n", path, err)
		os.Exit(1)
	}

	overview := taskui.NewTaskOverview(tracker)
	overview.OnEnter = func(task *taskui.Task, stop <-chan struct{}) {
		fmt.Fprintf(os.Stderr, "--- logs: %s (press q to return) ---\n", task.Name)

		printer := log.NewLogPrinter(task.Log)
		printer.Run()

		done := make(chan struct{})
		go func() {
			buf := make([]byte, 1)
			for {
				n, err := os.Stdin.Read(buf)
				if err != nil {
					break
				}
				if n == 1 && buf[0] == 'q' {
					break
				}
			}
			close(done)
		}()

		select {
		case <-done:
		case <-stop:
		}

		printer.Stop()
		fmt.Fprintf(os.Stderr, "--- end logs ---\n")
	}
	overview.Show()

	// Wait for Ctrl+C
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig

	overview.Hide()
}
