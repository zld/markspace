package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
)

// HashParts hashes length-prefixed parts so that concatenation ambiguity
// cannot produce the same digest for different inputs.
func HashParts(parts ...[]byte) string {
	h := sha256.New()
	var size [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(size[:], uint64(len(p)))
		h.Write(size[:])
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HashReader is the streaming form of HashParts for one large part.
func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := ioCopy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ioCopy(h hash.Hash, r io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var n int64
	for {
		k, err := r.Read(buf)
		h.Write(buf[:k])
		n += int64(k)
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// CanonicalJSON marshals v deterministically without HTML escaping.
func CanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}
