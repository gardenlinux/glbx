// Package objstore implements the content-addressed object store: blob storage
// keyed by the SHA-256 of the bytes, an identity-to-manifest map, and the
// identity primitives the rest of the system composes.
package objstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

// hashPattern matches exactly 64 lowercase hex characters.
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Hash is a validated SHA-256 digest. A constructed value is guaranteed to be
// exactly 64 lowercase hexadecimal characters. The zero value is not valid —
// construct with NewHash or MustHash.
//
// A Hash is an opaque token: nothing reads structure out of it beyond the
// sharding split and equality. It serves both as a blob's content address and
// as an artifact's identity, drawn from one space.
type Hash struct {
	hex string
}

// NewHash constructs a Hash from a hex string, returning an error if the input
// is not exactly 64 lowercase hex characters.
func NewHash(s string) (Hash, error) {
	if !hashPattern.MatchString(s) {
		return Hash{}, fmt.Errorf("invalid hash %q: must be exactly 64 lowercase hex characters", s)
	}
	return Hash{hex: s}, nil
}

// MustHash constructs a Hash from a hex string, panicking if invalid. Use only
// for compile-time constants and tests.
func MustHash(s string) Hash {
	h, err := NewHash(s)
	if err != nil {
		panic(err)
	}
	return h
}

// String returns the 64-character hex representation of the hash.
func (h Hash) String() string {
	return h.hex
}

// Short returns the first 12 characters of the hash for display.
func (h Hash) Short() string {
	if len(h.hex) >= 12 {
		return h.hex[:12]
	}
	return h.hex
}

// IsZero reports whether the hash is the zero (uninitialized) value.
func (h Hash) IsZero() bool {
	return h.hex == ""
}

// Equal reports whether two hashes are equal.
func (h Hash) Equal(other Hash) bool {
	return h.hex == other.hex
}

// Prefix returns the first 2 characters, the shard directory name.
func (h Hash) Prefix() string {
	return h.hex[:2]
}

// Suffix returns characters 3-64, the filename within the shard directory.
func (h Hash) Suffix() string {
	return h.hex[2:]
}

// HashBytes returns the SHA-256 of b as a Hash.
func HashBytes(b []byte) Hash {
	d := sha256.Sum256(b)
	return Hash{hex: hex.EncodeToString(d[:])}
}
