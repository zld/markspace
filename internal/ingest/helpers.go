package ingest

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// sha256Short returns the first 8 bytes of the SHA-256 sum as hex, matching
// the short content-hash convention used across the pipeline.
func sha256Short(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8])
}

// pcmBytes re-exports the little-endian PCM encoding for callers outside the
// MP4 reader that need a stable hash input.
func pcmBytes(pcm []int16) []byte {
	out := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}
