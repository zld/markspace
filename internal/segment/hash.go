package segment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"mediad/internal/ebml"
)

// ErrSpanMismatch reports a hash request whose spans are not contiguous. Hash
// results must be reproducible from exact input bytes, so gaps are rejected
// rather than papered over.
var ErrSpanMismatch = errors.New("segment: hash spans must be contiguous")

// HashSegment hashes the byte range [start, end) of the source, delegating the
// actual read to hashSpan. It exists so the segmenter stays independent of file
// I/O while still producing byte-exact digests.
func HashSegment(hashSpan func(ebml.Span) ([32]byte, error), start, end int64) (string, error) {
	if end < start {
		return "", fmt.Errorf("segment: inverted span [%d,%d)", start, end)
	}
	sum, err := hashSpan(ebml.Span{Offset: start, Length: end - start})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum[:]), nil
}

// HashSpans hashes a sorted set of spans as one digest, requiring them to be
// contiguous. This is used when a segment's bytes are not a single range.
func HashSpans(hashSpan func(ebml.Span) ([32]byte, error), spans []ebml.Span) (string, error) {
	if len(spans) == 0 {
		return hex.EncodeToString(make([]byte, 32)), nil
	}
	sorted := append([]ebml.Span(nil), spans...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Offset < sorted[j].Offset })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Offset != sorted[i-1].End() {
			return "", fmt.Errorf("%w: [%d,%d) then [%d,%d)",
				ErrSpanMismatch, sorted[i-1].Offset, sorted[i-1].End(), sorted[i].Offset, sorted[i].End())
		}
	}
	start, end := sorted[0].Offset, sorted[len(sorted)-1].End()
	return HashSegment(hashSpan, start, end)
}

// digestConfig serializes the config canonically and hashes it. encoding/json
// emits struct fields in declaration order, so the bytes are stable.
func digestConfig(c Config) string {
	b, err := json.Marshal(c)
	if err != nil {
		// Config has no unsupported fields; this cannot fail in practice.
		panic("segment: config marshal: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ManifestDigest returns the SHA-256 of the manifest's canonical JSON. Field
// order is struct declaration order, and segments are already in index order,
// so repeated runs over the same input yield the same digest.
func ManifestDigest(m *Manifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// MarshalCanonical returns the canonical JSON bytes for external verification.
// Map keys are sorted by encoding/json, and no timestamps or pointers leak in.
func MarshalCanonical(v any) ([]byte, error) {
	return json.Marshal(v)
}

// EqualReports compares two manifests field by field, including every segment
// hash, to prove round-trip consistency.
func EqualReports(a, b *Manifest) error {
	if a == nil || b == nil {
		return errors.New("segment: nil manifest")
	}
	if a.Representation != b.Representation || a.ConfigDigest != b.ConfigDigest || a.InputDigest != b.InputDigest {
		return errors.New("segment: manifest header differs")
	}
	if len(a.Segments) != len(b.Segments) {
		return fmt.Errorf("segment: segment count %d != %d", len(a.Segments), len(b.Segments))
	}
	for i := range a.Segments {
		x, y := a.Segments[i], b.Segments[i]
		if x.Index != y.Index || x.StartNS != y.StartNS || x.EndNS != y.EndNS ||
			x.VideoBlocks != y.VideoBlocks || x.AudioBlocks != y.AudioBlocks ||
			x.Keyframe != y.Keyframe || x.KeyframeNS != y.KeyframeNS || x.Hash != y.Hash {
			return fmt.Errorf("segment: segment %d differs", i)
		}
	}
	return nil
}
