package objstore

import (
	"crypto/sha256"
	"encoding/hex"
)

// ConcatHash folds an ordered tuple of parts into a single identity hash. Each
// part is SHA-256'd to 32 bytes, the digests are concatenated in order, and the
// concatenation is SHA-256'd. Reducing every part to a fixed 32-byte frame
// before concatenation makes the encoding unambiguous: no two distinct tuples
// produce the same pre-image, so moving a byte between adjacent parts always
// changes the result. Order is significant.
func ConcatHash(parts ...string) Hash {
	h := sha256.New()
	for _, part := range parts {
		digest := sha256.Sum256([]byte(part))
		h.Write(digest[:])
	}
	return Hash{hex: hex.EncodeToString(h.Sum(nil))}
}
