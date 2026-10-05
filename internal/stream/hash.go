package stream

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

// HashReader wraps an io.Reader and accumulates a SHA-256 digest of all data
// read through it. After reading is complete (or at any point), call Sum() to
// get the current hex-encoded digest.
type HashReader struct {
	reader io.Reader
	hash   hash.Hash
}

// NewHashReader creates a HashReader wrapping the given reader.
func NewHashReader(r io.Reader) *HashReader {
	return &HashReader{
		reader: r,
		hash:   sha256.New(),
	}
}

// Read reads from the underlying reader, updating the hash with the data read.
func (hr *HashReader) Read(p []byte) (int, error) {
	n, err := hr.reader.Read(p)
	if n > 0 {
		hr.hash.Write(p[:n])
	}
	return n, err
}

// Sum returns the hex-encoded SHA-256 digest of all data read so far.
func (hr *HashReader) Sum() string {
	return hex.EncodeToString(hr.hash.Sum(nil))
}

// HashWriter wraps an io.Writer and accumulates a SHA-256 digest of all data
// written through it.
type HashWriter struct {
	writer io.Writer
	hash   hash.Hash
}

// NewHashWriter creates a HashWriter wrapping the given writer.
func NewHashWriter(w io.Writer) *HashWriter {
	return &HashWriter{
		writer: w,
		hash:   sha256.New(),
	}
}

// Write writes data to the underlying writer, updating the hash.
func (hw *HashWriter) Write(p []byte) (int, error) {
	n, err := hw.writer.Write(p)
	if n > 0 {
		hw.hash.Write(p[:n])
	}
	return n, err
}

// Sum returns the hex-encoded SHA-256 digest of all data written so far.
func (hw *HashWriter) Sum() string {
	return hex.EncodeToString(hw.hash.Sum(nil))
}

// SHA256Reader reads the entirety of r and returns the hex-encoded SHA-256
// digest of the data. Returns an error if reading fails.
func SHA256Reader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SHA256Bytes computes the hex-encoded SHA-256 digest of the given byte slice.
func SHA256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
