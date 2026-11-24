package batch

import (
	"crypto/sha256"
	"os"

	"mediapack/internal/fault"
)

// Input builds the identity record the runner needs from raw input bytes,
// computing the size and SHA-256 used for input-change detection.
func Input(data []byte) *runInput {
	sum := sha256.Sum256(data)
	return &runInput{data: data, sum: sum, size: int64(len(data))}
}

func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fault.New(fault.KindIO, -1, "write corrupt index: %v", err)
	}
	return nil
}
