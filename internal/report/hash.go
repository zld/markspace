package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// marshalStable encodes v as JSON with map keys sorted and no HTML escaping,
// so the digest of a report depends only on its content and the order of its
// slices, never on map iteration order or struct field time.
func marshalStable(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// encoding/json already sorts map keys; strip the trailing newline so the
	// digest is over the document, not the framing.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// DigestBytes returns the lowercase hex SHA-256 of b.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SortedHashLines returns the per-segment hashes as sorted "uri sha256" lines,
// for a compact stable listing independent of manifest order.
func SortedHashLines(r *Report) []string {
	lines := make([]string, 0, len(r.Hashes))
	for _, h := range r.Hashes {
		lines = append(lines, h.URI+" "+h.SHA256)
	}
	sort.Strings(lines)
	return lines
}
