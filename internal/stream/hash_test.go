package stream

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func TestHashReader_Basic(t *testing.T) {
	data := "hello, world!"
	expected := sha256.Sum256([]byte(data))
	expectedHex := hex.EncodeToString(expected[:])

	hr := NewHashReader(strings.NewReader(data))

	// Read all data
	var buf bytes.Buffer
	n, err := io.Copy(&buf, hr)
	if err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if int(n) != len(data) {
		t.Errorf("read %d bytes, want %d", n, len(data))
	}
	if buf.String() != data {
		t.Errorf("data mismatch: got %q, want %q", buf.String(), data)
	}

	got := hr.Sum()
	if got != expectedHex {
		t.Errorf("hash = %s, want %s", got, expectedHex)
	}
}

func TestHashReader_Empty(t *testing.T) {
	expected := sha256.Sum256(nil)
	expectedHex := hex.EncodeToString(expected[:])

	hr := NewHashReader(strings.NewReader(""))
	io.Copy(io.Discard, hr)

	got := hr.Sum()
	if got != expectedHex {
		t.Errorf("hash = %s, want %s", got, expectedHex)
	}
}

func TestHashReader_IncrementalReads(t *testing.T) {
	data := "0123456789abcdef"
	expected := sha256.Sum256([]byte(data))
	expectedHex := hex.EncodeToString(expected[:])

	hr := NewHashReader(strings.NewReader(data))

	// Read in small chunks
	buf := make([]byte, 4)
	var total int
	for {
		n, err := hr.Read(buf)
		total += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}

	if total != len(data) {
		t.Errorf("total bytes read = %d, want %d", total, len(data))
	}

	got := hr.Sum()
	if got != expectedHex {
		t.Errorf("hash = %s, want %s", got, expectedHex)
	}
}

func TestHashReader_SumIsIdempotent(t *testing.T) {
	hr := NewHashReader(strings.NewReader("test"))
	io.Copy(io.Discard, hr)

	sum1 := hr.Sum()
	sum2 := hr.Sum()
	if sum1 != sum2 {
		t.Errorf("Sum() not idempotent: %s != %s", sum1, sum2)
	}
}

func TestHashWriter_Basic(t *testing.T) {
	data := "hello, world!"
	expected := sha256.Sum256([]byte(data))
	expectedHex := hex.EncodeToString(expected[:])

	var buf bytes.Buffer
	hw := NewHashWriter(&buf)

	n, err := hw.Write([]byte(data))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(data) {
		t.Errorf("wrote %d bytes, want %d", n, len(data))
	}

	// Verify data passed through
	if buf.String() != data {
		t.Errorf("passthrough data = %q, want %q", buf.String(), data)
	}

	got := hw.Sum()
	if got != expectedHex {
		t.Errorf("hash = %s, want %s", got, expectedHex)
	}
}

func TestHashWriter_MultipleWrites(t *testing.T) {
	parts := []string{"hello", ", ", "world", "!"}
	full := strings.Join(parts, "")
	expected := sha256.Sum256([]byte(full))
	expectedHex := hex.EncodeToString(expected[:])

	var buf bytes.Buffer
	hw := NewHashWriter(&buf)

	for _, part := range parts {
		hw.Write([]byte(part))
	}

	if buf.String() != full {
		t.Errorf("passthrough = %q, want %q", buf.String(), full)
	}

	got := hw.Sum()
	if got != expectedHex {
		t.Errorf("hash = %s, want %s", got, expectedHex)
	}
}

func TestHashWriter_PartialWrite(t *testing.T) {
	// Test with a writer that returns short writes
	data := []byte("partial write test data here")

	// limitedWriter simulates a writer that only writes 5 bytes at a time
	lw := &limitedWriter{max: 5}
	hw := NewHashWriter(lw)

	n, err := hw.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// The hash should only include bytes actually written
	expectedData := data[:n]
	expected := sha256.Sum256(expectedData)
	expectedHex := hex.EncodeToString(expected[:])

	got := hw.Sum()
	if got != expectedHex {
		t.Errorf("hash = %s, want %s", got, expectedHex)
	}
}

type limitedWriter struct {
	max int
	buf bytes.Buffer
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > lw.max {
		p = p[:lw.max]
	}
	return lw.buf.Write(p)
}

func TestSHA256Reader(t *testing.T) {
	data := "compute hash of entire reader"
	expected := sha256.Sum256([]byte(data))
	expectedHex := hex.EncodeToString(expected[:])

	got, err := SHA256Reader(strings.NewReader(data))
	if err != nil {
		t.Fatalf("SHA256Reader: %v", err)
	}
	if got != expectedHex {
		t.Errorf("got %s, want %s", got, expectedHex)
	}
}

func TestSHA256Reader_Empty(t *testing.T) {
	expected := sha256.Sum256(nil)
	expectedHex := hex.EncodeToString(expected[:])

	got, err := SHA256Reader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("SHA256Reader: %v", err)
	}
	if got != expectedHex {
		t.Errorf("got %s, want %s", got, expectedHex)
	}
}

func TestSHA256Reader_Large(t *testing.T) {
	// Test with a larger payload
	data := strings.Repeat("x", 1<<20) // 1 MiB
	expected := sha256.Sum256([]byte(data))
	expectedHex := hex.EncodeToString(expected[:])

	got, err := SHA256Reader(strings.NewReader(data))
	if err != nil {
		t.Fatalf("SHA256Reader: %v", err)
	}
	if got != expectedHex {
		t.Errorf("got %s, want %s", got, expectedHex)
	}
}

func TestSHA256Bytes(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{"empty", nil},
		{"hello", []byte("hello")},
		{"binary", []byte{0x00, 0x01, 0x02, 0xff}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected := sha256.Sum256(tt.input)
			expectedHex := hex.EncodeToString(expected[:])

			got := SHA256Bytes(tt.input)
			if got != expectedHex {
				t.Errorf("got %s, want %s", got, expectedHex)
			}
		})
	}
}

func TestHashReader_ConsistentWithSHA256Reader(t *testing.T) {
	data := "consistency check between HashReader and SHA256Reader"

	// Compute with HashReader
	hr := NewHashReader(strings.NewReader(data))
	io.Copy(io.Discard, hr)
	hashReaderResult := hr.Sum()

	// Compute with SHA256Reader
	sha256ReaderResult, err := SHA256Reader(strings.NewReader(data))
	if err != nil {
		t.Fatalf("SHA256Reader: %v", err)
	}

	// Compute with SHA256Bytes
	sha256BytesResult := SHA256Bytes([]byte(data))

	if hashReaderResult != sha256ReaderResult {
		t.Errorf("HashReader (%s) != SHA256Reader (%s)", hashReaderResult, sha256ReaderResult)
	}
	if hashReaderResult != sha256BytesResult {
		t.Errorf("HashReader (%s) != SHA256Bytes (%s)", hashReaderResult, sha256BytesResult)
	}
}

func TestSHA1Bytes(t *testing.T) {
	// Known vector.
	data := []byte("The quick brown fox jumps over the lazy dog")
	const want = "2fd4e1c67a2d28fced849ee1bb76e7391b93eb12"
	if got := SHA1Bytes(data); got != want {
		t.Errorf("SHA1Bytes = %q, want %q", got, want)
	}
}

func TestSHA1Reader(t *testing.T) {
	data := "The quick brown fox jumps over the lazy dog"
	const want = "2fd4e1c67a2d28fced849ee1bb76e7391b93eb12"
	got, err := SHA1Reader(strings.NewReader(data))
	if err != nil {
		t.Fatalf("SHA1Reader: %v", err)
	}
	if got != want {
		t.Errorf("SHA1Reader = %q, want %q", got, want)
	}
	if SHA1Bytes([]byte(data)) != got {
		t.Errorf("SHA1Reader and SHA1Bytes disagree")
	}
}
