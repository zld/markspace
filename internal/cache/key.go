// Package cache implements content-addressed stage artifacts. A cache key is
// derived from the input byte digest and the processing parameters; a hit is
// only accepted when source identity, input digest and artifact digest all
// match, otherwise the entry is discarded and rebuilt.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Params is the canonical set of processing inputs that must be identical
// for two runs to share a cache entry.
type Params struct {
	Channels      uint16 `json:"channels"`
	SampleRate    uint32 `json:"sample_rate"`
	BitsPerSample uint16 `json:"bits_per_sample"`
	BlockFrames   int64  `json:"block_frames"`
	ClockRate     uint32 `json:"clock_rate"`
	Layout        string `json:"layout"`
	StageVersion  string `json:"stage_version"`
}

// Key derives a hex SHA-256 cache key from the input digest and a
// deterministic JSON rendering of the processing parameters.
func Key(inputDigest string, p Params) string {
	h := sha256.New()
	h.Write([]byte(inputDigest))
	// json.Marshal on a struct with fixed field order is deterministic.
	raw, _ := json.Marshal(p)
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

// DigestBytes returns the hex SHA-256 of a byte payload.
func DigestBytes(b []byte) string {
	sum := sha256.New()
	sum.Write(b)
	return hex.EncodeToString(sum.Sum(nil))
}

// sortedKeys is a small helper retained for map-based parameter callers.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// KeyFromMap supports callers that assemble parameters dynamically; map keys
// are sorted before hashing so the result does not depend on iteration order.
func KeyFromMap(inputDigest string, m map[string]string) string {
	h := sha256.New()
	h.Write([]byte(inputDigest))
	for _, k := range sortedKeys(m) {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(m[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
