package log

import (
	"errors"
	"sync"
)

// ErrNoMore is returned by BufferReader.Read when all currently buffered
// records have been consumed. It is not permanent — more records may arrive.
var ErrNoMore = errors.New("no more records available")

// BufferTarget stores emitted records in memory. It serializes writers via a
// mutex and supports any number of readers, each tracking its own position.
type BufferTarget struct {
	mu      sync.Mutex
	cond    *sync.Cond
	records []Record
}

// NewBufferTarget creates an in-memory buffered target.
func NewBufferTarget() *BufferTarget {
	bt := &BufferTarget{}
	bt.cond = sync.NewCond(&bt.mu)
	return bt
}

// Emit appends a record and wakes waiting readers.
func (bt *BufferTarget) Emit(r Record) {
	bt.mu.Lock()
	bt.records = append(bt.records, r)
	bt.mu.Unlock()
	bt.cond.Broadcast()
}

// Len returns the number of buffered records.
func (bt *BufferTarget) Len() int {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	return len(bt.records)
}

// Reader returns a new reader positioned at the first record.
func (bt *BufferTarget) Reader() *BufferReader {
	return &BufferReader{buf: bt}
}

// Notify wakes all waiting readers, e.g. to let them re-check a stop condition.
func (bt *BufferTarget) Notify() {
	bt.cond.Broadcast()
}

// BufferReader reads records sequentially from a BufferTarget, tracking its own
// position.
type BufferReader struct {
	buf *BufferTarget
	pos int
}

// Read returns the next record, or ErrNoMore when caught up with the buffer.
func (r *BufferReader) Read() (Record, error) {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()

	if r.pos >= len(r.buf.records) {
		return Record{}, ErrNoMore
	}
	rec := r.buf.records[r.pos]
	r.pos++
	return rec, nil
}

// Wait blocks until records are available beyond the reader's position or the
// buffer is notified.
func (r *BufferReader) Wait() {
	r.buf.mu.Lock()
	for r.pos >= len(r.buf.records) {
		r.buf.cond.Wait()
	}
	r.buf.mu.Unlock()
}
