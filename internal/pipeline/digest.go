package pipeline

import (
	"crypto/sha256"
	"encoding/hex"

	"regress-kernel/internal/media"
)

// BuildInputSummary folds every consumed frame and audio packet into one
// stable content summary. It excludes arrival order so reordered-but-equal
// deliveries still describe the same task; payload bytes drive the digest.
func BuildInputSummary(frames []media.Frame, audio []media.AudioPacket,
	lossInjected int) (InputSummary, error) {
	sum := InputSummary{FrameCount: len(frames), LossInjected: lossInjected}
	h := sha256.New()
	var frameBytes int64
	for _, f := range frames {
		frameBytes += int64(len(f.Payload))
		h.Write([]byte("F"))
		h.Write(f.Payload)
		h.Write([]byte(f.DeclaredSHA256))
	}
	sum.InputBytes = frameBytes
	var err error
	if sum.FrameHash, err = hashToHex(h); err != nil {
		return sum, err
	}

	ah := sha256.New()
	for _, ap := range audio {
		ah.Write([]byte("A"))
		ah.Write(ap.Payload)
	}
	if sum.AudioHash, err = hashToHex(ah); err != nil {
		return sum, err
	}
	return sum, nil
}

// hashToHex finalizes a hash state into a lowercase hex string.
func hashToHex(h interface{ Sum([]byte) []byte }) (string, error) {
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FileSHA256 returns the lowercase hex digest of a byte slice.
func FileSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
