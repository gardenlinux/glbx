package log

import (
	"bufio"
	"context"
	"os"
	"sync"
)

// NewExecWriters returns two files to use as a subprocess's stdout and stderr.
// Lines written to stdout become Info records; lines on stderr become Warn
// records, both tagged with component c. Close the write ends after the child
// starts, then call closeFn to wait for the drain goroutines to finish.
func NewExecWriters(ctx context.Context, c Component) (stdout *os.File, stderr *os.File, closeFn func()) {
	l := From(ctx, c)

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scan := bufio.NewScanner(outR)
		for scan.Scan() {
			l.Info("%s", scan.Text())
		}
		outR.Close()
	}()

	go func() {
		defer wg.Done()
		scan := bufio.NewScanner(errR)
		for scan.Scan() {
			l.Warn("%s", scan.Text())
		}
		errR.Close()
	}()

	closeFn = func() {
		outW.Close()
		errW.Close()
		wg.Wait()
	}

	return outW, errW, closeFn
}
