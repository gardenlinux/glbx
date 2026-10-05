package stream

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DebExtractData opens a .deb file (which is an ar archive) and returns a
// reader for the data.tar.* member along with its filename (needed to select
// the correct decompressor). The caller should pass the returned reader and
// name to TarExtractCompressed.
func DebExtractData(r io.Reader) (io.Reader, string, error) {
	br := bufio.NewReader(r)

	// ar archives start with "!<arch>\n"
	magic := make([]byte, 8)
	if _, err := io.ReadFull(br, magic); err != nil {
		return nil, "", fmt.Errorf("read ar magic: %w", err)
	}
	if string(magic) != "!<arch>\n" {
		return nil, "", fmt.Errorf("not an ar archive (bad magic: %q)", magic)
	}

	// Read members until we find data.tar.*
	for {
		name, size, err := readArHeader(br)
		if err != nil {
			return nil, "", fmt.Errorf("read ar member: %w", err)
		}

		if strings.HasPrefix(name, "data.tar") {
			// Read the entire member into memory so we can return a self-contained reader
			data := make([]byte, size)
			if _, err := io.ReadFull(br, data); err != nil {
				return nil, "", fmt.Errorf("read data member: %w", err)
			}
			return bytes.NewReader(data), name, nil
		}

		// Skip this member (+ padding byte for odd sizes)
		skip := size
		if size%2 != 0 {
			skip++
		}
		if _, err := io.CopyN(io.Discard, br, int64(skip)); err != nil {
			return nil, "", fmt.Errorf("skip ar member %s: %w", name, err)
		}
	}
}

// readArHeader parses a single ar member header (60 bytes) and returns the
// member name and size.
func readArHeader(r io.Reader) (string, int64, error) {
	var hdr [60]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", 0, err
	}

	// ar header format: name[16] mtime[12] uid[6] gid[6] mode[8] size[10] magic[2]
	if hdr[58] != '`' || hdr[59] != '\n' {
		return "", 0, fmt.Errorf("invalid ar header magic")
	}

	name := strings.TrimRight(string(hdr[0:16]), " ")
	name = strings.TrimSuffix(name, "/")

	sizeStr := strings.TrimSpace(string(hdr[48:58]))
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("parse ar member size %q: %w", sizeStr, err)
	}

	return name, size, nil
}
