package pipeline

import (
	"bytes"
	"path/filepath"
)

// newByteSeeker adapts in-memory media bytes to the scanner's stream
// interface. The scanner seeks box by box, so peak memory is bounded by one
// fragment regardless of total media size.
func newByteSeeker(data []byte) *bytes.Reader { return bytes.NewReader(data) }

// mediaPath returns the on-disk location used for media of a representation.
func mediaPath(workDir, repID string) string {
	return filepath.Join(workDir, "inputs", repID+".mp4")
}
