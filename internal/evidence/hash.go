// Package evidence provides streaming content hashes used by source, track,
// segment, and published-artifact evidence records.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"os"
)

// SHA256Bytes returns the hex SHA-256 of b.
func SHA256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SHA256Reader streams r through SHA-256 without buffering the whole input.
func SHA256Reader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// SHA256File streams a local file and returns its hex digest and size.
func SHA256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return SHA256Reader(f)
}

// NewHasher returns the standard hash used for evidence records.
func NewHasher() hash.Hash { return sha256.New() }

// HashJSON deterministically hashes a JSON-serializable value. Struct field
// order is fixed by Go, and maps are sorted by encoding/json.
func HashJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return SHA256Bytes(b), nil
}

// HashParts hashes length-prefixed textual/binary parts. Length prefixes make
// the result unambiguous when fields or bytes contain separators.
func HashParts(parts ...[]byte) string {
	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range parts {
		putUint64BE(lenBuf[:], uint64(len(p)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func putUint64BE(b []byte, v uint64) {
	b[0] = byte(v >> 56)
	b[1] = byte(v >> 48)
	b[2] = byte(v >> 40)
	b[3] = byte(v >> 32)
	b[4] = byte(v >> 24)
	b[5] = byte(v >> 16)
	b[6] = byte(v >> 8)
	b[7] = byte(v)
}
