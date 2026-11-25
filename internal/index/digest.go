// Package index implements content/parameter keys and deterministic rebuild
// decisions.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sort"
)

// SHA256Hex returns the lowercase 64-character digest of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// FileSHA256 streams a file and returns its lowercase digest.
func FileSHA256(path string) (string, error) {
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

// CanonicalJSON renders values with deterministic object key order. encoding/json
// already sorts map keys; the trailing newline makes hashing shell-friendly.
func CanonicalJSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParamsDigest returns the stable digest of encapsulation parameters.
func ParamsDigest(params any) (string, error) {
	b, err := CanonicalJSON(params)
	if err != nil {
		return "", err
	}
	return SHA256Hex(b), nil
}

// SortedKeys is a small helper for deterministic diagnostic lists.
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
