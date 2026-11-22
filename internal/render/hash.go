package render

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// FileDigest returns the hex SHA-256 of one file.
func FileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SegmentDigest computes the per-segment hash: SHA-256 over the
// concatenated frame file bytes in frame order, domain-separated by
// the segment index so frames cannot be shifted between segments.
func SegmentDigest(segIndex int, framePaths []string) (string, error) {
	h := sha256.New()
	if _, err := h.Write([]byte{byte(segIndex), byte(segIndex >> 8)}); err != nil {
		return "", err
	}
	for _, p := range framePaths {
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// BytesDigest is the hex SHA-256 of an in-memory blob.
func BytesDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
