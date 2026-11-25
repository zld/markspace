// Package fingerprint provides content hashes and stable task-key derivation.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sort"
)

const Algorithm = "sha256"

type Hash struct {
	Algorithm string `json:"algorithm"`
	SHA256    string `json:"sha256"`
}

func Bytes(b []byte) Hash {
	sum := sha256.Sum256(b)
	return Hash{Algorithm: Algorithm, SHA256: hex.EncodeToString(sum[:])}
}

func Hex(b []byte) string { return Bytes(b).SHA256 }

func Reader(r io.Reader) (Hash, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return Hash{}, err
	}
	return Hash{Algorithm: Algorithm, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func File(path string) (Hash, error) {
	f, err := os.Open(path)
	if err != nil {
		return Hash{}, err
	}
	defer f.Close()
	return Reader(f)
}

// Digest canonicalizes JSON values by sorting map keys through encoding/json's
// deterministic encoder and hashing the resulting bytes.
func Digest(v any) (Hash, []byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return Hash{}, nil, err
	}
	return Bytes(b), b, nil
}

// StableTaskKey creates a short, filesystem-safe stable key from a logical
// task name and input fingerprints. It never includes wall-clock state.
func StableTaskKey(scope string, inputHashes []string) string {
	cp := append([]string(nil), inputHashes...)
	sort.Strings(cp)
	h := sha256.New()
	_, _ = io.WriteString(h, scope)
	_, _ = h.Write([]byte{0})
	for _, item := range cp {
		_, _ = io.WriteString(h, item)
		_, _ = h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

// MergeHashes returns the distinct sorted hashes from all groups.
func MergeHashes(groups ...[]Hash) []Hash {
	seen := make(map[string]Hash)
	for _, group := range groups {
		for _, h := range group {
			if h.SHA256 != "" {
				seen[h.Algorithm+":"+h.SHA256] = h
			}
		}
	}
	out := make([]Hash, 0, len(seen))
	for _, h := range seen {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SHA256 < out[j].SHA256 })
	return out
}
