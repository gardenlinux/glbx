package objstore

import (
	"testing"
)

func TestNewHash_Valid(t *testing.T) {
	input := "9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53"
	h, err := NewHash(input)
	if err != nil {
		t.Fatalf("NewHash(%q) returned error: %v", input, err)
	}
	if h.String() != input {
		t.Errorf("String() = %q, want %q", h.String(), input)
	}
}

func TestNewHash_AllZeroes(t *testing.T) {
	input := "0000000000000000000000000000000000000000000000000000000000000000"
	h, err := NewHash(input)
	if err != nil {
		t.Fatalf("NewHash(%q) returned error: %v", input, err)
	}
	if h.String() != input {
		t.Errorf("String() = %q, want %q", h.String(), input)
	}
}

func TestNewHash_InvalidCases(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"too short", "9a09690faf8b2b09"},
		{"too long", "9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be530"},
		{"uppercase", "9A09690FAF8B2B09CB02BE917387A121BE291AF320548A1600B5105BC646BE53"},
		{"mixed case", "9a09690faf8b2b09cb02be917387a121be291AF320548a1600b5105bc646be53"},
		{"invalid chars", "zz09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53"},
		{"spaces", " a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53"},
		{"trailing space", "9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be5 "},
		{"newline", "9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be5\n"},
		{"63 chars", "a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewHash(tc.input)
			if err == nil {
				t.Errorf("NewHash(%q) should return error", tc.input)
			}
		})
	}
}

func TestMustHash_Valid(t *testing.T) {
	input := "fb81856a93eb6572d2c9e43c06a52c7cc17f47506dee98059ab90c8240444abb"
	h := MustHash(input)
	if h.String() != input {
		t.Errorf("MustHash(%q).String() = %q", input, h.String())
	}
}

func TestMustHash_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustHash with invalid input should panic")
		}
	}()
	MustHash("invalid")
}

func TestHash_IsZero(t *testing.T) {
	var h Hash
	if !h.IsZero() {
		t.Error("zero-value Hash should report IsZero() == true")
	}
	h = MustHash("9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53")
	if h.IsZero() {
		t.Error("valid Hash should report IsZero() == false")
	}
}

func TestHash_Equal(t *testing.T) {
	a := MustHash("9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53")
	b := MustHash("9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53")
	c := MustHash("fb81856a93eb6572d2c9e43c06a52c7cc17f47506dee98059ab90c8240444abb")

	if !a.Equal(b) {
		t.Error("identical hashes should be equal")
	}
	if a.Equal(c) {
		t.Error("different hashes should not be equal")
	}
}

func TestHash_PrefixSuffix(t *testing.T) {
	h := MustHash("9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53")
	if h.Prefix() != "9a" {
		t.Errorf("Prefix() = %q, want %q", h.Prefix(), "9a")
	}
	if h.Suffix() != "09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53" {
		t.Errorf("Suffix() = %q, want correct 62 chars", h.Suffix())
	}
	if h.Prefix()+h.Suffix() != h.String() {
		t.Error("Prefix() + Suffix() should equal String()")
	}
}

func TestHash_Short(t *testing.T) {
	h := MustHash("9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53")
	if h.Short() != "9a09690faf8b" {
		t.Errorf("Short() = %q, want %q", h.Short(), "9a09690faf8b")
	}
}

func TestHashBytes(t *testing.T) {
	// SHA-256("hello world\n") is a known value.
	h := HashBytes([]byte("hello world\n"))
	want := "a948904f2f0f479b8f8197694b30184b0d2ed1c1cd2a1ec0fb85d299a192a447"
	if h.String() != want {
		t.Errorf("HashBytes = %q, want %q", h.String(), want)
	}
	if HashBytes([]byte("a")).Equal(HashBytes([]byte("b"))) {
		t.Error("different inputs should hash differently")
	}
}
