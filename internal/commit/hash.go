package commit

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"

	"mediapack/internal/manifest"
)

func newSHA() hash.Hash { return sha256.New() }

func hexSum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

// verifyFile reports whether path exists with the expected hash and length.
// A zero wantLen skips the length check.
func verifyFile(path, wantSHA string, wantLen int64) (bool, error) {
	got, n, err := manifest.HashFile(path)
	if err != nil {
		return false, err
	}
	if wantLen > 0 && n != wantLen {
		return false, nil
	}
	return got == wantSHA, nil
}
