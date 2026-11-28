package main

import (
	"crypto/sha256"
	"encoding/hex"

	"rtp-rebuild/internal/manifest"
)

// digest is the lowercase hex SHA-256 of data.
func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// digestStrings hashes ordered strings with a domain tag.
func digestStrings(tag string, values ...string) string {
	return manifest.DigestStrings(tag, values...)
}
