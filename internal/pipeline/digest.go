package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// digestFile hashes a file with a fixed-size buffer so verification does not
// depend on file size. The 64 KiB buffer matches the pipeline-wide chunk size.
func digestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
