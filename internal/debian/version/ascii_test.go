package version

import (
	"testing"
)

// TestCharOrderASCIIOnly verifies that the character ordering function uses
// ASCII-only letter classification. Per deb-version(7), only ASCII letters
// (a-z, A-Z) are treated as "letters" in the ordering. Non-ASCII bytes (which
// can appear in version strings as part of filenames or locale quirks) should
// sort as non-letter characters. Using unicode.IsLetter() instead of an
// ASCII-only check would misclassify bytes like 0xC0-0xFF (Latin-1 supplement
// letters like À, Ñ, etc.) as "letters", changing their sort position.
func TestCharOrderASCIIOnly(t *testing.T) {
	// Bytes 0xC0-0xFF are Unicode letters in Latin-1 Supplement but should NOT
	// be treated as letters by Debian version comparison (dpkg uses ASCII-only).
	// If unicode.IsLetter is used, these would return int(c) instead of int(c)+256.
	nonASCIILetters := []byte{
		0xC0, // À
		0xC1, // Á
		0xD1, // Ñ
		0xE0, // à
		0xE9, // é
		0xF1, // ñ
		0xFF, // ÿ
	}

	for _, c := range nonASCIILetters {
		order := charOrder(c, true)
		// Per deb-version(7), non-letter characters sort AFTER all letters.
		// ASCII letters have values 65-122, so their charOrder returns 65-122.
		// Non-letters should return int(c) + 256 (i.e., > 256).
		// If unicode.IsLetter is incorrectly used, these bytes (192-255) would
		// return their raw value (192-255), which sorts AFTER 'z' (122) but
		// BEFORE the expected 256+ range.
		if order < 256 {
			t.Errorf("charOrder(0x%02X) = %d; non-ASCII bytes should sort as non-letters (>= 256).\n"+
				"  This is caused by using unicode.IsLetter() instead of an ASCII-only check.\n"+
				"  dpkg treats only a-z, A-Z as letters; bytes >= 0x80 are non-letter non-tilde.",
				c, order)
		}
	}

	// Verify ASCII letters still work correctly
	asciiLetters := []byte{'a', 'z', 'A', 'Z', 'm', 'M'}
	for _, c := range asciiLetters {
		order := charOrder(c, true)
		if order >= 256 || order < 0 {
			t.Errorf("charOrder('%c') = %d; ASCII letters should sort in range [1, 255]", c, order)
		}
	}

	// Verify non-letter ASCII characters sort after letters
	nonLetterASCII := []byte{'.', '+', '-', '_', ':', '~'}
	for _, c := range nonLetterASCII {
		order := charOrder(c, true)
		if c == '~' {
			if order != -1 {
				t.Errorf("charOrder('~') = %d; should be -1", order)
			}
		} else {
			if order < 256 {
				t.Errorf("charOrder('%c') = %d; non-letter ASCII should sort >= 256", c, order)
			}
		}
	}
}

// TestCompareNonASCIIBytes tests that version strings containing non-ASCII bytes
// are compared correctly (as non-letters).
func TestCompareNonASCIIBytes(t *testing.T) {
	// A version with a non-ASCII byte should treat it as a non-letter.
	// Non-letters sort AFTER all letters.
	// So "1.0\xc0" should sort AFTER "1.0z" (since \xc0 is non-letter, z is letter).
	v1 := "1.0z"
	v2 := "1.0\xc0" // Latin-1 'À' byte

	cmp := Compare(v1, v2)
	// Letters sort before non-letters, so 'z' < '\xc0' in Debian ordering.
	// Expected: v1 < v2 (cmp == -1)
	if cmp != -1 {
		t.Errorf("Compare(%q, %q) = %d; expected -1.\n"+
			"  'z' is a letter and should sort BEFORE non-ASCII byte 0xC0.\n"+
			"  If unicode.IsLetter is used, 0xC0 (À) is incorrectly classified as a letter\n"+
			"  and sorts by its raw value (192) instead of value+256 (448).",
			v1, v2, cmp)
	}
}

// TestCompareNonASCIIBytesAmongLetters verifies that when unicode.IsLetter
// misclassifies high bytes as letters, it corrupts the sort order.
func TestCompareNonASCIIBytesAmongLetters(t *testing.T) {
	// If unicode.IsLetter is used: charOrder(0xE9) = 233 (raw byte value)
	// This would sort between 'z' (122) and non-letters (256+), effectively
	// treating it as a "super-z" letter.
	//
	// Correct behavior: charOrder(0xE9) = 233 + 256 = 489 (non-letter).
	// This means 0xE9 sorts after ALL letters, same as '.', '+', etc.

	// With correct ASCII-only logic: 'z' (letter, order=122) < '.' (non-letter, order=46+256=302)
	// With buggy unicode logic: same result for these specific chars.
	// But: 'z' (letter, order=122) vs 0xE9:
	//   Correct: 'z'(122) < 0xE9(489) → "1.0z" < "1.0\xe9"
	//   Buggy:   'z'(122) < 0xE9(233) → "1.0z" < "1.0\xe9" (same direction but wrong magnitude)

	// The real problem shows with comparison between non-ASCII "letters" and
	// actual non-letter ASCII:
	// Correct: 0xE9 (non-letter, 489) > '.' (non-letter, 302)
	// Buggy:   0xE9 (letter, 233) < '.' (non-letter, 302)  ← WRONG!

	v1 := "1.0\xe9" // should be non-letter
	v2 := "1.0."    // '.' is non-letter, order = 46+256 = 302

	cmp := Compare(v1, v2)
	// Correct: both are non-letters, 0xe9+256=489 > 46+256=302, so v1 > v2
	// Buggy (unicode.IsLetter): 0xe9 is "letter" with order 233, '.' is non-letter
	//   with order 302, so v1 < v2. WRONG!
	if cmp != 1 {
		t.Errorf("Compare(%q, %q) = %d; expected 1.\n"+
			"  0xE9 should be a non-letter (order=489), '.' is non-letter (order=302).\n"+
			"  So 0xE9 > '.'. If this fails, unicode.IsLetter is misclassifying 0xE9.",
			v1, v2, cmp)
	}
}
