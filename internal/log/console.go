package log

import (
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiDim    = "\033[2m"
)

// startTime anchors the elapsed-time prefix on every record. Records carry no
// timestamp of their own; elapsed is derived here.
var startTime = time.Now()

// ConsoleTarget writes records to stdio: INFO to stdout (unless stderrOnly),
// everything else to stderr, with TTY-aware coloring on stderr.
type ConsoleTarget struct {
	mu         sync.Mutex
	colorErr   bool
	stderrOnly bool
}

// NewConsoleTarget returns a console target routing INFO to stdout.
func NewConsoleTarget() *ConsoleTarget {
	return &ConsoleTarget{colorErr: term.IsTerminal(int(os.Stderr.Fd()))}
}

// NewStderrConsoleTarget routes every level, INFO included, to stderr. Use it
// for commands whose stdout carries machine-parseable output.
func NewStderrConsoleTarget() *ConsoleTarget {
	return &ConsoleTarget{
		colorErr:   term.IsTerminal(int(os.Stderr.Fd())),
		stderrOnly: true,
	}
}

func (c *ConsoleTarget) Emit(r Record) {
	c.mu.Lock()
	defer c.mu.Unlock()

	comp := r.Component.String()
	ts := time.Since(startTime).Truncate(time.Millisecond)

	infoOut := os.Stdout
	if c.stderrOnly {
		infoOut = os.Stderr
	}

	switch r.Level {
	case Info:
		fmt.Fprintf(infoOut, "%s [INFO] %s: %s\n", ts, comp, r.Msg)
	case Debug:
		writeColored(os.Stderr, c.colorErr, ansiDim, ts, "DEBUG", comp, r.Msg)
	case Warn:
		writeColored(os.Stderr, c.colorErr, ansiYellow, ts, "WARN", comp, r.Msg)
	case Error:
		writeColored(os.Stderr, c.colorErr, ansiRed, ts, "ERROR", comp, r.Msg)
	}
}

func writeColored(f *os.File, color bool, ansi string, ts time.Duration, level, comp, msg string) {
	if color {
		fmt.Fprintf(f, "%s%s [%s] %s: %s%s\n", ansi, ts, level, comp, msg, ansiReset)
	} else {
		fmt.Fprintf(f, "%s [%s] %s: %s\n", ts, level, comp, msg)
	}
}
