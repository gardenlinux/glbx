package log

import (
	"encoding/json"
	"io"
	"sync"
)

type serializedRecord struct {
	Level     Level     `json:"level"`
	Component Component `json:"component"`
	Msg       string    `json:"msg"`
}

// MarshalJSON serializes all buffered records as a JSON array.
func (bt *BufferTarget) MarshalJSON() ([]byte, error) {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	recs := make([]serializedRecord, len(bt.records))
	for i, r := range bt.records {
		recs[i] = serializedRecord{Level: r.Level, Component: r.Component, Msg: r.Msg}
	}
	return json.Marshal(recs)
}

// UnmarshalJSON replaces the buffer's records with a deserialized JSON array.
func (bt *BufferTarget) UnmarshalJSON(data []byte) error {
	var recs []serializedRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return err
	}

	bt.mu.Lock()
	defer bt.mu.Unlock()

	bt.records = make([]Record, len(recs))
	for i, r := range recs {
		bt.records[i] = Record{Level: r.Level, Component: r.Component, Msg: r.Msg}
	}
	if bt.cond == nil {
		bt.cond = sync.NewCond(&bt.mu)
	}
	return nil
}

// WriteTo serializes the buffer to w.
func (bt *BufferTarget) WriteTo(w io.Writer) (int64, error) {
	data, err := bt.MarshalJSON()
	if err != nil {
		return 0, err
	}
	n, err := w.Write(data)
	return int64(n), err
}

// ReadFrom deserializes the buffer from r.
func (bt *BufferTarget) ReadFrom(r io.Reader) (int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	return int64(len(data)), bt.UnmarshalJSON(data)
}
