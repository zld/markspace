// Package index computes content digests and maintains the local asset
// index with chunked, resumable checkpoints. Digests cover both source
// bytes and container/track metadata, so any change — content, track
// parameters, or muxing — invalidates exactly the affected assets.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sort"

	"releasepipe/internal/errs"
)

// MaxDigestFileBytes caps files fed into the digest.
const MaxDigestFileBytes = 1 << 30

// HashBytes digests the concatenation of parts.
func HashBytes(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HashStrings digests length-prefixed strings (unambiguous joining).
func HashStrings(parts ...string) string {
	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range parts {
		n := uint64(len(p))
		for i := 0; i < 8; i++ {
			lenBuf[i] = byte(n >> (8 * i))
		}
		h.Write(lenBuf[:])
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HashMeta digests a key/value map in canonical sorted-key order.
func HashMeta(kv map[string]string) string {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		parts = append(parts, k, kv[k])
	}
	return HashStrings(parts...)
}

// HashFile streams a file into a digest, rejecting oversize inputs.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errs.Wrap(errs.StageIndex, errs.CodeInternal, path, -1, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", errs.Wrap(errs.StageIndex, errs.CodeInternal, path, -1, err)
	}
	if st.Size() > MaxDigestFileBytes {
		return "", errs.New(errs.StageIndex, errs.CodeInternal, path, MaxDigestFileBytes,
			"file too large to digest: %d bytes", st.Size())
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", errs.Wrap(errs.StageIndex, errs.CodeInternal, path, -1, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
