// Package shortcode converts a monotonic counter into a fixed-length,
// non-sequential, alphanumeric short code using XOR + multiplicative hashing
// followed by base62 encoding.
//
// The mapping is deterministic and reversible: the same counter + xorKey pair
// always produces the same code.  Codes look random to observers because
// adjacent counter values produce visually unrelated codes.
//
// Capacity: 62^8 = 218,340,105,584,896 (~218 trillion) unique codes.
// Counter values beyond that limit wrap (mod 62^8), so in practice the
// counter should never approach that range for a URL shortener.
package shortcode

const (
	alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	codeLen  = 8
	base     = uint64(62)

	// 62^8 — the total number of representable codes.
	space = uint64(218_340_105_584_896)

	// Knuth multiplicative hash constant (odd, fills 64 bits well).
	// Multiplying by this before taking mod space spreads counter values
	// across the full output range.
	hashMul = uint64(6_364_136_223_846_793_005)
)

// Encode maps counter to an 8-character base62 string.
//   - counter: monotonically increasing value from the raft-replicated counter.
//   - xorKey:  deployment-specific secret that ensures codes from different
//     deployments don't collide and are not guessable from the counter alone.
func Encode(counter uint64, xorKey uint64) string {
	v := scramble(counter, xorKey)
	return toBase62(v)
}

// scramble applies XOR with the key, a multiplicative mix, and a finalisation
// XOR-shift so that the output is uniformly distributed within [0, space).
func scramble(counter, xorKey uint64) uint64 {
	v := counter ^ xorKey
	// Multiplicative hash — natural uint64 overflow acts as mod 2^64.
	v *= hashMul
	// Finalisation: fold the high bits back to remove correlation.
	v ^= v >> 33
	// Bring into the 62^8 space.
	return v % space
}

// toBase62 encodes v as exactly codeLen base62 digits, zero-padded on the left.
func toBase62(v uint64) string {
	var buf [codeLen]byte
	for i := codeLen - 1; i >= 0; i-- {
		buf[i] = alphabet[v%base]
		v /= base
	}
	return string(buf[:])
}
