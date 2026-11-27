// Package archive stores verified assets under a local root, keyed by the
// SHA-256 of the source content. Writes are atomic and repeated deliveries of
// the same bytes reuse the existing result instead of appending duplicates.
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Digest is a lowercase hex SHA-256 fingerprint.
type Digest = string

const manifestName = "archive.json"

// Entry is one archived asset.
type Entry struct {
	// Digest is the source-content fingerprint and the idempotency key.
	Digest Digest `json:"digest"`
	// BlobDigest is the fingerprint of the archived container bytes and is
	// verified against the bytes handed to Put or Commit.
	BlobDigest  Digest  `json:"blob_digest"`
	Codec       string  `json:"codec"`
	Bytes       int64   `json:"bytes"`
	Blob        string  `json:"blob"`
	PSNRdB      float64 `json:"psnr_db"`
	Verified    bool    `json:"verified"`
	CommittedAt int64   `json:"committed_at_ns"`
}

// DigestOf computes the SHA-256 of data.
func DigestOf(data []byte) Digest {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DigestString normalises a user supplied digest.
func DigestString(s string) Digest { return strings.ToLower(strings.TrimSpace(s)) }

// Summary renders an entry for terminal output.
func (e Entry) Summary() string {
	return fmt.Sprintf("%s codec=%s bytes=%d psnr=%.2f verified=%v",
		shortDigest(e.Digest), e.Codec, e.Bytes, e.PSNRdB, e.Verified)
}

func shortDigest(d Digest) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
