package fixture

// CorruptMXFKLVLength overwrites the first KLV item's BER length with a long
// form value that declares more bytes than the file contains. The returned
// slice is a fresh copy so the caller's input is untouched.
func CorruptMXFKLVLength(data []byte) []byte {
	out := append([]byte(nil), data...)
	// Header partition pack: 16-byte key, then BER length at offset 16.
	if len(out) < 19 {
		return out
	}
	out[16] = 0x82
	out[17] = 0xFF
	out[18] = 0xFF
	return out
}

// TruncateBytes returns a copy shortened to n bytes (or empty).
func TruncateBytes(data []byte, n int) []byte {
	if n > len(data) {
		n = len(data)
	}
	if n < 0 {
		n = 0
	}
	return append([]byte(nil), data[:n]...)
}
