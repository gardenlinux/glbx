package stream

import (
	"fmt"
	"io"
	"strings"
)

// XZDecompress decompresses an xz-compressed stream.
// Spawns `xz -d -c` as an external process.
func XZDecompress(r io.Reader) (io.ReadCloser, error) {
	return newProcReadCloser(r, "xz", "-d", "-c")
}

// GzipDecompress decompresses a gzip-compressed stream.
// Spawns `gzip -d -c` as an external process.
func GzipDecompress(r io.Reader) (io.ReadCloser, error) {
	return newProcReadCloser(r, "gzip", "-d", "-c")
}

// Bzip2Decompress decompresses a bzip2-compressed stream.
// Spawns `bzip2 -d -c` as an external process.
func Bzip2Decompress(r io.Reader) (io.ReadCloser, error) {
	return newProcReadCloser(r, "bzip2", "-d", "-c")
}

// ZstdDecompress decompresses a zstd-compressed stream.
// Spawns `zstd -d -c` as an external process.
func ZstdDecompress(r io.Reader) (io.ReadCloser, error) {
	return newProcReadCloser(r, "zstd", "-d", "-c")
}

// AutoDecompress selects the appropriate decompressor based on filename extension.
// Supported extensions: .xz, .gz, .bz2, .zst
// If the extension is not recognized, returns an error.
func AutoDecompress(r io.Reader, filename string) (io.ReadCloser, error) {
	switch {
	case strings.HasSuffix(filename, ".xz"):
		return XZDecompress(r)
	case strings.HasSuffix(filename, ".gz"):
		return GzipDecompress(r)
	case strings.HasSuffix(filename, ".bz2"):
		return Bzip2Decompress(r)
	case strings.HasSuffix(filename, ".zst"):
		return ZstdDecompress(r)
	default:
		return nil, fmt.Errorf("stream: unsupported compression extension in %q", filename)
	}
}
