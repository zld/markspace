package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

func dirOf(path string) string { return filepath.Dir(path) }

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}
