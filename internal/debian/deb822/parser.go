// Package deb822 implements a streaming parser for deb822 format (RFC 822-like),
// used by Debian Packages, Sources, and Release files.
package deb822

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Stanza represents a single deb822 stanza as a map of field names to values.
// Field names are normalized to lowercase.
type Stanza map[string]string

// Reader is a streaming parser for deb822 format.
type Reader struct {
	scanner *bufio.Scanner
	// peeked holds a line that was read but not yet consumed.
	peeked  string
	hasPeek bool
	done    bool
}

// NewReader creates a new deb822 Reader from an io.Reader.
func NewReader(r io.Reader) *Reader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	return &Reader{
		scanner: scanner,
	}
}

// Next returns the next stanza from the input. Returns io.EOF when no more
// stanzas are available.
func (r *Reader) Next() (Stanza, error) {
	if r.done {
		return nil, io.EOF
	}

	stanza := make(Stanza)
	var currentField string

	for {
		line, ok := r.readLine()
		if !ok {
			// End of input
			r.done = true
			if len(stanza) == 0 {
				return nil, io.EOF
			}
			// Trim trailing newline from last field value
			if currentField != "" {
				stanza[currentField] = trimTrailingNewline(stanza[currentField])
			}
			return stanza, nil
		}

		// Comment lines are skipped entirely
		if strings.HasPrefix(line, "#") {
			continue
		}

		// Empty line (or line with only whitespace) separates stanzas
		if strings.TrimSpace(line) == "" {
			if len(stanza) == 0 {
				// Skip leading empty lines between stanzas
				continue
			}
			// End of current stanza
			if currentField != "" {
				stanza[currentField] = trimTrailingNewline(stanza[currentField])
			}
			return stanza, nil
		}

		// Continuation line: starts with space or tab
		if line[0] == ' ' || line[0] == '\t' {
			if currentField == "" {
				return nil, fmt.Errorf("deb822: continuation line before any field: %q", line)
			}
			// Append continuation line preserving the newline structure
			stanza[currentField] += "\n" + line
			continue
		}

		// Field line: Name: Value
		colonIdx := strings.IndexByte(line, ':')
		if colonIdx < 0 {
			return nil, fmt.Errorf("deb822: line without colon and not a continuation: %q", line)
		}

		name := line[:colonIdx]
		if err := validateFieldName(name); err != nil {
			return nil, err
		}

		// Normalize field name to lowercase
		normalizedName := strings.ToLower(name)

		// Value is everything after ": " (colon followed by optional space)
		value := ""
		if colonIdx+1 < len(line) {
			value = line[colonIdx+1:]
			// Strip single leading space after colon (standard format is "Field: Value")
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
		}

		currentField = normalizedName
		stanza[normalizedName] = value
	}
}

// readLine returns the next line from the scanner, using the peek buffer if available.
func (r *Reader) readLine() (string, bool) {
	if r.hasPeek {
		r.hasPeek = false
		return r.peeked, true
	}
	if r.scanner.Scan() {
		return r.scanner.Text(), true
	}
	return "", false
}

// validateFieldName checks that a field name contains only valid characters:
// printable ASCII excluding colon and whitespace.
func validateFieldName(name string) error {
	if name == "" {
		return fmt.Errorf("deb822: empty field name")
	}
	for _, c := range name {
		if c <= 0x20 || c > 0x7E || c == ':' {
			if unicode.IsSpace(c) {
				return fmt.Errorf("deb822: field name contains whitespace: %q", name)
			}
			return fmt.Errorf("deb822: field name contains invalid character %q: %q", c, name)
		}
	}
	return nil
}

// trimTrailingNewline removes a single trailing newline if present.
func trimTrailingNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}
