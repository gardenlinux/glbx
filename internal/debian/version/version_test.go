package version

import (
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		input    string
		expected Version
		wantErr  bool
	}{
		// Basic cases
		{"1.0", Version{0, "1.0", ""}, false},
		{"1.0-1", Version{0, "1.0", "1"}, false},
		{"1:1.0-1", Version{1, "1.0", "1"}, false},
		{"0:1.0-1", Version{0, "1.0", "1"}, false},

		// No epoch, no revision
		{"3.14.159", Version{0, "3.14.159", ""}, false},

		// Epoch only
		{"2:1.0", Version{2, "1.0", ""}, false},
		{"10:5.3", Version{10, "5.3", ""}, false},

		// Multiple hyphens - last one separates revision
		{"1.0-beta-1", Version{0, "1.0-beta", "1"}, false},
		{"1.0-2-3", Version{0, "1.0-2", "3"}, false},

		// Multiple colons - first one separates epoch
		{"1:2:3", Version{1, "2:3", ""}, false},
		{"1:2:3-4", Version{1, "2:3", "4"}, false},

		// Version with tilde
		{"1.0~beta1", Version{0, "1.0~beta1", ""}, false},
		{"1.0~beta1-1", Version{0, "1.0~beta1", "1"}, false},

		// Real-world versions
		{"2:3.36.1-2+deb11u1", Version{2, "3.36.1", "2+deb11u1"}, false},
		{"5.10.0-8", Version{0, "5.10.0", "8"}, false},
		{"1:9.18.19-1~deb12u1", Version{1, "9.18.19", "1~deb12u1"}, false},

		// Upstream with letters
		{"1.0a", Version{0, "1.0a", ""}, false},
		{"1.0.dfsg.1-1", Version{0, "1.0.dfsg.1", "1"}, false},

		// GardenLinux version format
		{"2.36.1-8+deb12u1+gl~abcdef01", Version{0, "2.36.1", "8+deb12u1+gl~abcdef01"}, false},

		// Error cases
		{"", Version{}, true},
		{"a:1.0", Version{}, true},  // non-numeric epoch
		{"-1:1.0", Version{}, true}, // negative epoch
		{"1:", Version{}, true},     // empty upstream
		{":1.0", Version{}, true},   // empty epoch string = non-numeric
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := Parse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Parse(%q) = %v, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.input, err)
			}
			if got != tt.expected {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		version  Version
		expected string
	}{
		{Version{0, "1.0", ""}, "1.0"},
		{Version{0, "1.0", "1"}, "1.0-1"},
		{Version{1, "1.0", "1"}, "1:1.0-1"},
		{Version{0, "1.0", "2+deb12u1"}, "1.0-2+deb12u1"},
		{Version{2, "3.36.1", "2+deb11u1"}, "2:3.36.1-2+deb11u1"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := tt.version.String()
			if got != tt.expected {
				t.Errorf("Version%+v.String() = %q, want %q", tt.version, got, tt.expected)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	// Pairs where a < b (Compare(a,b) should return -1)
	lessThan := []struct {
		a, b string
	}{
		// Basic numeric comparison
		{"1.0", "1.1"},
		{"1.0", "2.0"},
		{"1.9", "1.10"},   // numeric, not lexicographic
		{"1.009", "1.10"}, // leading zeros stripped
		{"0.9", "1.0"},

		// Epoch comparison dominates
		{"1:1.0", "2:0.1"},
		{"0:99.0", "1:0.1"},
		{"99.0", "1:0.1"}, // implicit epoch 0

		// Tilde sorts before everything
		{"1.0~", "1.0"},
		{"1.0~beta", "1.0"},
		{"1.0~beta1", "1.0"},
		{"1.0~alpha", "1.0~beta"},
		{"1.0~1", "1.0~2"},
		{"1.0~rc1", "1.0"},
		{"1.0~~", "1.0~"},
		{"1.0~", "1.0."},

		// Tilde in revision
		{"1.0-1~bpo", "1.0-1"},

		// Revision comparison
		{"1.0-1", "1.0-2"},
		{"1.0-9", "1.0-10"},

		// Empty revision sorts before any revision? No:
		// Per dpkg, empty revision is equivalent to "0" comparison
		// Actually: empty revision string sorts with the same algorithm
		// compareDebianString("", "1") — "" vs "1" in the digit part:
		// non-digit part: both empty, ok. digit part: "" (=0) < "1"
		// But let's test real dpkg behavior:
		{"1.0", "1.0-1"}, // empty revision < "1" (no digit = 0 < 1)

		// Letters sort before non-letters (., +, -, etc.)
		{"1.0a", "1.0."},
		{"1.0z", "1.0."},
		{"1.0A", "1.0."},
		{"1.0Z", "1.0."},

		// Among letters, lowercase vs uppercase follows ASCII
		// In Debian: letters are ordered by byte value
		{"1.0A", "1.0a"}, // 'A'(65) < 'a'(97)

		// Non-letter non-tilde characters by ASCII
		{"1.0+", "1.0."}, // '+' (43) < '.' (46) → both are non-letter, compare by ASCII+256
		// Actually: '+' (43+256=299) vs '.' (46+256=302), so + < .
		{"1.0-1+deb12u1", "1.0-1+deb12u2"},

		// Real-world version ordering (dpkg-verified)
		{"1.0-1", "1.0-2"},
		{"1.0.1-1", "1.0.2-1"},
		{"3.0-1", "3.0.1-1"},
		{"2.6.32-1", "2.6.32-2"},
		{"1:7.4p1-10+deb11u1", "1:7.4p1-10+deb11u2"},

		// Upstream with letters vs digits
		{"1.0", "1.0a"}, // "" vs "a" in non-digit: empty(0) < letter

		// GardenLinux version sorts above Debian
		{"2.36.1-8+deb12u1", "2.36.1-8+deb12u1+gl~abcdef01"},
	}

	for _, tt := range lessThan {
		t.Run(tt.a+"_lt_"+tt.b, func(t *testing.T) {
			cmp := Compare(tt.a, tt.b)
			if cmp != -1 {
				t.Errorf("Compare(%q, %q) = %d, want -1", tt.a, tt.b, cmp)
			}
			// Also verify reverse
			cmp = Compare(tt.b, tt.a)
			if cmp != 1 {
				t.Errorf("Compare(%q, %q) = %d, want 1", tt.b, tt.a, cmp)
			}
		})
	}

	// Equality cases
	equal := []struct {
		a, b string
	}{
		{"1.0", "1.0"},
		{"0:1.0", "1.0"}, // explicit epoch 0 = implicit epoch 0
		{"1:1.0-1", "1:1.0-1"},
		{"1.0-0", "1.0-0"},
		{"01.0", "1.0"}, // numeric comparison: leading zeros stripped in digit parts
	}

	for _, tt := range equal {
		t.Run(tt.a+"_eq_"+tt.b, func(t *testing.T) {
			cmp := Compare(tt.a, tt.b)
			if cmp != 0 {
				t.Errorf("Compare(%q, %q) = %d, want 0", tt.a, tt.b, cmp)
			}
		})
	}
}

func TestCompareTildeEdgeCases(t *testing.T) {
	// Tilde handling is the most commonly misimplemented part
	tests := []struct {
		a, b string
		want int
	}{
		// Tilde sorts before empty (end of string)
		{"1.0~", "1.0", -1},
		// Tilde sorts before any character
		{"1.0~a", "1.0a", -1},
		// Multiple tildes
		{"1.0~~", "1.0~", -1},
		{"1.0~~a", "1.0~a", -1},
		// Tilde followed by digits vs letters
		{"1.0~1", "1.0~a", -1}, // '1' is a digit, starts digit comparison: 1 vs letters...
		// Actually after the tilde: "~1" vs "~a"
		// non-digit: ~ == ~, then "1" is digit and "a" is non-digit
		// so the non-digit segment for the left is empty after ~, right has 'a' after ~
		// Wait: let me trace through more carefully
		// comparing "0~1" vs "0~a":
		// non-digit part: '~' vs '~' → equal
		// continue non-digit: '1' is digit (stop), 'a' is non-digit (continue)
		// so left has empty, right has 'a': order(empty)=0 vs order('a')=97 → left < right
		{"1.0~1", "1.0~a", -1},

		// Versions with tilde in upstream
		{"1.0~beta1", "1.0~beta2", -1},
		{"1.0~beta1", "1.0~rc1", -1}, // 'b' < 'r'
		{"1.0~alpha", "1.0~beta", -1},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := Compare(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCompareNumericSegments(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		// Leading zeros don't matter
		{"1.01", "1.1", 0},
		{"1.001", "1.1", 0},
		{"1.0010", "1.10", 0},
		// Numeric comparison, not lexicographic
		{"1.9", "1.10", -1},
		{"1.99", "1.100", -1},
		{"2.0", "10.0", -1},
		// Empty digit segment is 0
		{"1.0", "1.0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := Compare(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCheckConstraint(t *testing.T) {
	tests := []struct {
		version    string
		op         string
		constraint string
		want       bool
	}{
		// Strictly less than
		{"1.0", "<<", "2.0", true},
		{"2.0", "<<", "1.0", false},
		{"1.0", "<<", "1.0", false},

		// Less than or equal
		{"1.0", "<=", "2.0", true},
		{"1.0", "<=", "1.0", true},
		{"2.0", "<=", "1.0", false},

		// Equal
		{"1.0", "=", "1.0", true},
		{"1.0", "=", "2.0", false},
		{"0:1.0", "=", "1.0", true},

		// Greater than or equal
		{"2.0", ">=", "1.0", true},
		{"1.0", ">=", "1.0", true},
		{"1.0", ">=", "2.0", false},

		// Strictly greater than
		{"2.0", ">>", "1.0", true},
		{"1.0", ">>", "1.0", false},
		{"1.0", ">>", "2.0", false},

		// Real-world constraints
		{"2.36.1-8+deb12u1", ">=", "2.36.1-8", true},
		{"1:7.4p1-10+deb11u2", ">>", "1:7.4p1-10+deb11u1", true},
		{"1.0~beta1-1", "<<", "1.0-1", true},
	}

	for _, tt := range tests {
		t.Run(tt.version+"_"+tt.op+"_"+tt.constraint, func(t *testing.T) {
			got, err := CheckConstraint(tt.version, tt.op, tt.constraint)
			if err != nil {
				t.Fatalf("CheckConstraint(%q, %q, %q) error: %v",
					tt.version, tt.op, tt.constraint, err)
			}
			if got != tt.want {
				t.Errorf("CheckConstraint(%q, %q, %q) = %v, want %v",
					tt.version, tt.op, tt.constraint, got, tt.want)
			}
		})
	}
}

func TestCheckConstraintInvalidOp(t *testing.T) {
	_, err := CheckConstraint("1.0", ">", "2.0")
	if err == nil {
		t.Error("CheckConstraint with invalid op '>' should return error")
	}
	_, err = CheckConstraint("1.0", "<", "2.0")
	if err == nil {
		t.Error("CheckConstraint with invalid op '<' should return error")
	}
	_, err = CheckConstraint("1.0", "!=", "2.0")
	if err == nil {
		t.Error("CheckConstraint with invalid op '!=' should return error")
	}
}

func TestVersionCompareMethod(t *testing.T) {
	v1, _ := Parse("1:2.0-3")
	v2, _ := Parse("1:2.0-4")
	v3, _ := Parse("2:1.0-1")

	if v1.Compare(v2) != -1 {
		t.Error("v1 should be less than v2")
	}
	if v2.Compare(v1) != 1 {
		t.Error("v2 should be greater than v1")
	}
	if v1.Compare(v1) != 0 {
		t.Error("v1 should equal itself")
	}
	if v1.Compare(v3) != -1 {
		t.Error("epoch 1 should be less than epoch 2")
	}
}

func TestParseRoundTrip(t *testing.T) {
	versions := []string{
		"1.0",
		"1.0-1",
		"1:1.0-1",
		"2:3.36.1-2+deb11u1",
		"1.0~beta1-1",
		"5.10.0-8",
		"1:9.18.19-1~deb12u1",
	}

	for _, s := range versions {
		t.Run(s, func(t *testing.T) {
			v, err := Parse(s)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", s, err)
			}
			got := v.String()
			if got != s {
				t.Errorf("Parse(%q).String() = %q, want %q", s, got, s)
			}
		})
	}
}

// TestDpkgCompatibility tests known version orderings that match dpkg behavior.
// These are version pairs where dpkg --compare-versions confirms the ordering.
func TestDpkgCompatibility(t *testing.T) {
	// All pairs: left < right (dpkg --compare-versions left lt right)
	pairs := []struct {
		a, b string
	}{
		// From dpkg test suite / Debian policy examples
		{"1.0-1", "1.0-2"},

		{"1.0-0", "1.0-1"},
		{"0.0-0", "0.1-0"},
		{"1.0~beta1", "1.0"},
		{"1.0~beta1", "1.0~beta2"},
		{"1.0~beta1", "1.0~rc1"},
		{"1.2.3", "1.2.4"},
		{"1.2.3", "1.2.10"},
		{"2.0", "10.0"},
		{"0.1", "0.2"},
		{"1:0.1", "1:0.2"},
		{"0.1", "1:0.1"},
		{"1.0.1", "1.0.2"},
		{"1.0", "1.0.1"}, // "1.0" vs "1.0.1": after "1" match, "." vs "." match, "0" vs "0" match, then "" vs "." → empty(0) vs '.'(46+256) → left < right
		{"1.0+really1.0", "1.1"},
	}

	for _, tt := range pairs {
		t.Run(tt.a+"_lt_"+tt.b, func(t *testing.T) {
			cmp := Compare(tt.a, tt.b)
			if cmp != -1 {
				t.Errorf("Compare(%q, %q) = %d, want -1 (dpkg compatible)", tt.a, tt.b, cmp)
			}
		})
	}

	// Equal pairs
	equalPairs := []struct {
		a, b string
	}{
		{"1.0", "1.0"},
		{"0:1.0", "1.0"},
		{"1:1.0", "1:1.0"},
		// These should be equal per dpkg: empty digit = 0
		{"1.0", "1.0-0"},
	}

	for _, tt := range equalPairs {
		t.Run(tt.a+"_eq_"+tt.b, func(t *testing.T) {
			cmp := Compare(tt.a, tt.b)
			if cmp != 0 {
				t.Errorf("Compare(%q, %q) = %d, want 0 (dpkg compatible)", tt.a, tt.b, cmp)
			}
		})
	}
}

func TestLetterVsNonLetterOrdering(t *testing.T) {
	// Per deb-version(7): letters sort before non-letters (except ~)
	// So 'a' < '.' < no specific rule... let's verify:
	// Letters (a-z, A-Z) sort before non-letter non-tilde chars
	tests := []struct {
		a, b string
		want int
	}{
		// Letter sorts before '.'
		{"1.0a1", "1.0.1", -1},
		// Letter sorts before '+'
		{"1.0a1", "1.0+1", -1},
		// Letter sorts before '-' (in upstream, hyphen can appear before last)
		// Actually hyphen in upstream only if there's a revision. Let's use '+'
		{"1.0z1", "1.0+1", -1},

		// Among non-letters (non-tilde), sort by ASCII value
		{"1.0+1", "1.0.1", -1}, // '+' (43) < '.' (46)
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := Compare(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func BenchmarkCompare(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Compare("1:2.36.1-8+deb12u1+gl~abcdef01", "1:2.36.1-8+deb12u2+gl~12345678")
	}
}

func BenchmarkParse(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Parse("1:2.36.1-8+deb12u1+gl~abcdef01")
	}
}
