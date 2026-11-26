package resume

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
)

// SHA256Bytes returns the lowercase hex SHA-256 of data.
func SHA256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SHA256File streams a local file and returns its lowercase hex SHA-256.
func SHA256File(path string) (string, error) {
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

// PrefixSHA256 hashes at most prefixBytes from path. If prefixBytes <= 0 or
// exceeds the file size, the complete file is hashed and its actual length is
// returned.
func PrefixSHA256(path string, prefixBytes int64) (digest string, n int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	if prefixBytes > 0 {
		n, err = io.CopyN(h, f, prefixBytes)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			err = nil
		}
	} else {
		n, err = io.Copy(h, f)
	}
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// PrefixHasher incrementally builds the checkpoint input-prefix digest.
type PrefixHasher struct {
	h     hash.Hash
	bytes int64
}

// NewPrefixHasher creates an incremental SHA-256 prefix hasher.
func NewPrefixHasher() *PrefixHasher {
	return &PrefixHasher{h: sha256.New()}
}

// Write appends input bytes to the prefix.
func (p *PrefixHasher) Write(b []byte) (int, error) {
	n, err := p.h.Write(b)
	p.bytes += int64(n)
	return n, err
}

// Len returns the number of hashed bytes.
func (p *PrefixHasher) Len() int64 { return p.bytes }

// Sum returns the lowercase hex digest.
func (p *PrefixHasher) Sum() string {
	return hex.EncodeToString(p.h.Sum(nil))
}
