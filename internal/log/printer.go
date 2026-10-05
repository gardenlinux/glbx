package log

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// LogPrinter drains a BufferTarget to stdio using the console formatting. It
// runs as a goroutine, prints from the start of the buffer, waits when caught
// up, and exits once stopped and all records are drained.
type LogPrinter struct {
	buf      *BufferTarget
	reader   *BufferReader
	colorErr bool
	start    time.Time

	stopFlag atomic.Bool
	done     chan struct{}
	once     sync.Once
}

// NewLogPrinter creates a printer for buf. Call Run to start it.
func NewLogPrinter(buf *BufferTarget) *LogPrinter {
	return &LogPrinter{
		buf:      buf,
		reader:   buf.Reader(),
		colorErr: term.IsTerminal(int(os.Stderr.Fd())),
		start:    startTime,
		done:     make(chan struct{}),
	}
}

// Run starts the printer goroutine.
func (p *LogPrinter) Run() {
	go p.loop()
}

// Stop signals termination after draining remaining records and blocks until
// the goroutine exits.
func (p *LogPrinter) Stop() {
	p.once.Do(func() {
		p.stopFlag.Store(true)
		p.buf.Notify()
	})
	<-p.done
}

func (p *LogPrinter) loop() {
	defer close(p.done)
	for {
		rec, err := p.reader.Read()
		if err == ErrNoMore {
			if p.stopFlag.Load() {
				return
			}
			p.waitForData()
			continue
		}
		p.printRecord(rec)
	}
}

func (p *LogPrinter) waitForData() {
	p.buf.mu.Lock()
	for p.reader.pos >= len(p.buf.records) {
		if p.stopFlag.Load() {
			p.buf.mu.Unlock()
			return
		}
		p.buf.cond.Wait()
	}
	p.buf.mu.Unlock()
}

func (p *LogPrinter) printRecord(r Record) {
	comp := r.Component.String()
	ts := time.Since(p.start).Truncate(time.Millisecond)

	switch r.Level {
	case Info:
		fmt.Fprintf(os.Stdout, "%s [INFO] %s: %s\n", ts, comp, r.Msg)
	case Debug:
		writeColored(os.Stderr, p.colorErr, ansiDim, ts, "DEBUG", comp, r.Msg)
	case Warn:
		writeColored(os.Stderr, p.colorErr, ansiYellow, ts, "WARN", comp, r.Msg)
	case Error:
		writeColored(os.Stderr, p.colorErr, ansiRed, ts, "ERROR", comp, r.Msg)
	}
}
