// Package fingerprint computes deterministic, content-derived fingerprints
// for normalized samples. It covers both the raw fragment bytes and an
// offline "decoded frame" summary derived without any graphical dependency:
// pixel mean/variance for video and peak dBFS for audio.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
)

// FragmentHash returns the hex SHA-256 of a fragment's payload bytes.
// Identical bytes always hash identically, which is what lets the archive
// dedup repeated deliveries.
func FragmentHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// FrameHash returns the hex SHA-256 of a decoded-frame summary buffer.
func FrameHash(frame []byte) string {
	sum := sha256.Sum256(frame)
	return hex.EncodeToString(sum[:])
}

// StableKey builds a deterministic stage key from an input digest and the
// stage parameters. The same input plus the same parameters always yields
// the same key, so re-delivered work cannot create duplicate manifest rows
// or change artifact hashes.
func StableKey(stage, inputDigest string, params string) string {
	h := sha256.New()
	h.Write([]byte(stage))
	h.Write([]byte{0})
	h.Write([]byte(inputDigest))
	h.Write([]byte{0})
	h.Write([]byte(params))
	return hex.EncodeToString(h.Sum(nil))
}
