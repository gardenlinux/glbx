// Package version implements Debian version comparison as specified in
// deb-version(7). It provides parsing, formatting, and comparison of
// Debian package version strings.
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Version represents a parsed Debian version string with the format:
// [epoch:]upstream_version[-debian_revision]
type Version struct {
	Epoch    int
	Upstream string
	Revision string
}

// Parse parses a Debian version string into its component parts.
// The format is: [epoch:]upstream_version[-debian_revision]
//   - Epoch: optional integer before the first ':'. Default 0 if absent.
//   - Debian revision: everything after the LAST '-'. Empty if no '-'.
//   - Upstream version: everything between epoch and revision.
func Parse(s string) (Version, error) {
	if s == "" {
		return Version{}, fmt.Errorf("empty version string")
	}

	var v Version

	// Extract epoch: everything before the first ':'
	if idx := strings.IndexByte(s, ':'); idx >= 0 {
		epochStr := s[:idx]
		epoch, err := strconv.Atoi(epochStr)
		if err != nil {
			return Version{}, fmt.Errorf("invalid epoch %q: %w", epochStr, err)
		}
		if epoch < 0 {
			return Version{}, fmt.Errorf("negative epoch: %d", epoch)
		}
		v.Epoch = epoch
		s = s[idx+1:]
	}

	// Extract debian revision: everything after the LAST '-'
	if idx := strings.LastIndexByte(s, '-'); idx >= 0 {
		v.Revision = s[idx+1:]
		s = s[:idx]
	}

	// What remains is the upstream version
	v.Upstream = s

	if v.Upstream == "" {
		return Version{}, fmt.Errorf("empty upstream version")
	}

	return v, nil
}

// String returns the canonical string representation of the version.
// Epoch is omitted if zero, revision is omitted if empty.
func (v Version) String() string {
	var b strings.Builder
	if v.Epoch != 0 {
		b.WriteString(strconv.Itoa(v.Epoch))
		b.WriteByte(':')
	}
	b.WriteString(v.Upstream)
	if v.Revision != "" {
		b.WriteByte('-')
		b.WriteString(v.Revision)
	}
	return b.String()
}

// Compare compares two Version values using the Debian version comparison
// algorithm. Returns -1 if v < other, 0 if v == other, +1 if v > other.
func (v Version) Compare(other Version) int {
	// 1. Compare epochs numerically
	if v.Epoch < other.Epoch {
		return -1
	}
	if v.Epoch > other.Epoch {
		return 1
	}

	// 2. Compare upstream versions
	if c := compareDebianString(v.Upstream, other.Upstream); c != 0 {
		return c
	}

	// 3. Compare debian revisions
	return compareDebianString(v.Revision, other.Revision)
}

// Compare parses two version strings and compares them.
// Returns -1, 0, or 1. If either string is unparseable, they are
// compared lexicographically as a fallback.
func Compare(a, b string) int {
	va, errA := Parse(a)
	vb, errB := Parse(b)
	if errA != nil || errB != nil {
		// Fallback to string comparison for invalid versions
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
	return va.Compare(vb)
}

// CheckConstraint checks whether a version satisfies a dependency constraint.
// op must be one of: ">>", ">=", "=", "<=", "<<"
func CheckConstraint(version string, op string, constraint string) (bool, error) {
	cmp := Compare(version, constraint)

	switch op {
	case "<<":
		return cmp < 0, nil
	case "<=":
		return cmp <= 0, nil
	case "=":
		return cmp == 0, nil
	case ">=":
		return cmp >= 0, nil
	case ">>":
		return cmp > 0, nil
	default:
		return false, fmt.Errorf("unknown version operator %q (expected <<, <=, =, >=, or >>)", op)
	}
}

// compareDebianString implements the Debian version string comparison
// algorithm as described in deb-version(7). It compares two strings by
// alternating between non-digit and digit segments.
func compareDebianString(a, b string) int {
	ia, ib := 0, 0

	for ia < len(a) || ib < len(b) {
		// Compare non-digit parts
		c := compareNonDigitPart(a, b, &ia, &ib)
		if c != 0 {
			return c
		}

		// Compare digit parts (numerically)
		c = compareDigitPart(a, b, &ia, &ib)
		if c != 0 {
			return c
		}
	}
	return 0
}

// compareNonDigitPart compares the non-digit prefix of both strings starting
// at the given indices, advancing them past the non-digit segment.
func compareNonDigitPart(a, b string, ia, ib *int) int {
	for {
		ca, okA := peekNonDigit(a, *ia)
		cb, okB := peekNonDigit(b, *ib)

		if !okA && !okB {
			// Both segments are exhausted
			return 0
		}

		oa := charOrder(ca, okA)
		ob := charOrder(cb, okB)

		if oa < ob {
			return -1
		}
		if oa > ob {
			return 1
		}

		// Advance indices past consumed characters
		if okA {
			*ia++
		}
		if okB {
			*ib++
		}
	}
}

// peekNonDigit returns the character at position i if it's a non-digit,
// and a boolean indicating whether a character was available.
func peekNonDigit(s string, i int) (byte, bool) {
	if i >= len(s) {
		return 0, false
	}
	c := s[i]
	if c >= '0' && c <= '9' {
		return 0, false
	}
	return c, true
}

// charOrder returns the sort order value for a character in non-digit
// comparison. The Debian ordering is:
//
//	~ < (empty/end) < letters (a-zA-Z) < everything else
//
// We encode this as an integer for comparison:
//   - ~     → -1
//   - empty → 0  (handled by the caller)
//   - letters → value 1..52
//   - other → value 256 + ASCII
func charOrder(c byte, present bool) int {
	if !present {
		// Empty/end of segment
		return 0
	}
	if c == '~' {
		return -1
	}
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
		return int(c)
	}
	// Non-letter, non-tilde: sort after letters
	// All ASCII non-letter values are above 'z' (122) except some,
	// so we add 256 to ensure they sort after all letters.
	return int(c) + 256
}

// compareDigitPart compares the digit prefix of both strings numerically,
// advancing the indices past the digit segment.
func compareDigitPart(a, b string, ia, ib *int) int {
	// Extract digit segments
	numA := extractDigits(a, ia)
	numB := extractDigits(b, ib)

	// Strip leading zeros for numeric comparison.
	// Per dpkg: an empty digit segment has numeric value 0.
	numA = stripLeadingZeros(numA)
	numB = stripLeadingZeros(numB)

	// Normalize: empty string and "0" both represent zero
	if numA == "" {
		numA = "0"
	}
	if numB == "" {
		numB = "0"
	}

	// Compare by length first (longer number with no leading zeros is larger)
	if len(numA) < len(numB) {
		return -1
	}
	if len(numA) > len(numB) {
		return 1
	}

	// Same length: compare lexicographically (works for same-length digit strings)
	if numA < numB {
		return -1
	}
	if numA > numB {
		return 1
	}
	return 0
}

// extractDigits extracts and returns the digit prefix starting at *i,
// advancing *i past the digits.
func extractDigits(s string, i *int) string {
	start := *i
	for *i < len(s) && s[*i] >= '0' && s[*i] <= '9' {
		*i++
	}
	return s[start:*i]
}

// stripLeadingZeros removes leading zeros from a numeric string.
// Returns "0" for empty string or all-zeros.
func stripLeadingZeros(s string) string {
	if s == "" {
		return ""
	}
	i := 0
	for i < len(s)-1 && s[i] == '0' {
		i++
	}
	return s[i:]
}
