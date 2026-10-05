package objstore

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestConcatHash_SinglePart(t *testing.T) {
	part := "hello"
	inner := sha256.Sum256([]byte(part))
	outer := sha256.Sum256(inner[:])
	expected := hex.EncodeToString(outer[:])

	result := ConcatHash(part)
	if result.String() != expected {
		t.Errorf("ConcatHash(%q) = %s, want %s", part, result, expected)
	}
}

func TestConcatHash_MultipleParts(t *testing.T) {
	da := sha256.Sum256([]byte("a"))
	db := sha256.Sum256([]byte("b"))
	combined := append(da[:], db[:]...)
	outer := sha256.Sum256(combined)
	expected := hex.EncodeToString(outer[:])

	result := ConcatHash("a", "b")
	if result.String() != expected {
		t.Errorf("ConcatHash(\"a\", \"b\") = %s, want %s", result, expected)
	}
}

func TestConcatHash_Empty(t *testing.T) {
	outer := sha256.Sum256(nil)
	expected := hex.EncodeToString(outer[:])

	result := ConcatHash()
	if result.String() != expected {
		t.Errorf("ConcatHash() = %s, want %s", result, expected)
	}
}

func TestConcatHash_OrderMatters(t *testing.T) {
	ab := ConcatHash("a", "b")
	ba := ConcatHash("b", "a")
	if ab.Equal(ba) {
		t.Error("ConcatHash should be order-dependent")
	}
}

// A framed fold must not confuse a boundary shift: ("ab","c") and ("a","bc")
// are distinct tuples and must not collide, which plain concatenation would.
func TestConcatHash_FramingUnambiguous(t *testing.T) {
	if ConcatHash("ab", "c").Equal(ConcatHash("a", "bc")) {
		t.Error("boundary-shifted tuples must not collide")
	}
}

func TestConcatHash_DifferentInputsDifferentOutput(t *testing.T) {
	if ConcatHash("input1").Equal(ConcatHash("input2")) {
		t.Error("different inputs should produce different hashes")
	}
}

func TestConcatHash_Deterministic(t *testing.T) {
	if !ConcatHash("a", "b", "c").Equal(ConcatHash("a", "b", "c")) {
		t.Error("same inputs should produce same hash")
	}
}

func TestConcatHash_ResultIsValidHash(t *testing.T) {
	h := ConcatHash("test")
	if _, err := NewHash(h.String()); err != nil {
		t.Errorf("ConcatHash result is not a valid hash: %v", err)
	}
}
