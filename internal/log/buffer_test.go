package log

import (
	"sync"
	"testing"
	"time"
)

func TestBufferTargetEmitAndRead(t *testing.T) {
	bt := NewBufferTarget()
	bt.Emit(Record{Level: Info, Component: Build, Msg: "hello"})
	bt.Emit(Record{Level: Debug, Component: Engine, Msg: "debug msg"})

	if bt.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", bt.Len())
	}

	r := bt.Reader()
	rec, err := r.Read()
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if rec.Level != Info || rec.Msg != "hello" {
		t.Errorf("rec = %+v, want Info/hello", rec)
	}
	rec, err = r.Read()
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if rec.Level != Debug || rec.Msg != "debug msg" {
		t.Errorf("rec = %+v, want Debug/debug msg", rec)
	}
	if _, err = r.Read(); err != ErrNoMore {
		t.Errorf("Read() at end = %v, want ErrNoMore", err)
	}
}

func TestBufferReaderEOFIsNotPermanent(t *testing.T) {
	bt := NewBufferTarget()
	r := bt.Reader()

	if _, err := r.Read(); err != ErrNoMore {
		t.Fatalf("expected ErrNoMore on empty buffer, got %v", err)
	}
	bt.Emit(Record{Level: Warn, Component: Fetch, Msg: "new data"})
	rec, err := r.Read()
	if err != nil {
		t.Fatalf("Read() after new emit: %v", err)
	}
	if rec.Level != Warn || rec.Msg != "new data" {
		t.Errorf("rec = %+v", rec)
	}
}

func TestBufferMultipleReaders(t *testing.T) {
	bt := NewBufferTarget()
	bt.Emit(Record{Level: Info, Component: Build, Msg: "one"})
	bt.Emit(Record{Level: Info, Component: Build, Msg: "two"})

	r1 := bt.Reader()
	r2 := bt.Reader()

	rec1, _ := r1.Read()
	rec2, _ := r2.Read()
	if rec1.Msg != "one" || rec2.Msg != "one" {
		t.Errorf("both readers should start at 0: r1=%q r2=%q", rec1.Msg, rec2.Msg)
	}
	rec1, _ = r1.Read()
	if rec1.Msg != "two" {
		t.Errorf("r1 second read = %q, want two", rec1.Msg)
	}
	rec2, _ = r2.Read()
	if rec2.Msg != "two" {
		t.Errorf("r2 second read = %q, want two", rec2.Msg)
	}
}

func TestBufferConcurrentWriters(t *testing.T) {
	bt := NewBufferTarget()
	const numWriters = 10
	const msgsPerWriter = 100

	var wg sync.WaitGroup
	wg.Add(numWriters)
	for i := range numWriters {
		go func(id int) {
			defer wg.Done()
			for j := range msgsPerWriter {
				bt.Emit(Record{Level: Info, Component: Build, Msg: time.Duration(id*1000 + j).String()})
			}
		}(i)
	}
	wg.Wait()

	if bt.Len() != numWriters*msgsPerWriter {
		t.Errorf("Len() = %d, want %d", bt.Len(), numWriters*msgsPerWriter)
	}

	r := bt.Reader()
	count := 0
	for {
		if _, err := r.Read(); err == ErrNoMore {
			break
		}
		count++
	}
	if count != numWriters*msgsPerWriter {
		t.Errorf("read %d records, want %d", count, numWriters*msgsPerWriter)
	}
}

func TestBufferReaderWait(t *testing.T) {
	bt := NewBufferTarget()
	r := bt.Reader()

	done := make(chan struct{})
	go func() {
		r.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Wait returned before any emit")
	case <-time.After(50 * time.Millisecond):
	}

	bt.Emit(Record{Level: Info, Component: Build, Msg: "wake up"})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after emit")
	}

	rec, err := r.Read()
	if err != nil || rec.Msg != "wake up" {
		t.Errorf("after Wait: rec=%+v err=%v", rec, err)
	}
}

func TestBufferNotifyWakesWaiters(t *testing.T) {
	bt := NewBufferTarget()
	r := bt.Reader()

	woken := make(chan struct{})
	go func() {
		r.Wait()
		close(woken)
	}()

	select {
	case <-woken:
		t.Fatal("Wait returned before any emit")
	case <-time.After(50 * time.Millisecond):
	}

	bt.Emit(Record{Level: Debug, Component: Build, Msg: "second"})

	select {
	case <-woken:
	case <-time.After(time.Second):
		t.Fatal("waiter not woken after emit")
	}
}
