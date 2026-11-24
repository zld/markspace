// Package manifest builds and writes the deterministic aligned segment list.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/zld/avpipe/internal/align"
	"github.com/zld/avpipe/internal/metadata"
	"github.com/zld/avpipe/internal/mp4"
)

// Version is the manifest schema version.
const Version = "avpipe.v1"

// QuarantinedSegment records a fragment excluded because samples are unreadable.
type QuarantinedSegment struct {
	Representation        string `json:"representation"`
	Sequence              uint32 `json:"sequence"`
	BoxOffset             int64  `json:"box_offset"`
	FirstUnreadableOffset int64  `json:"first_unreadable_offset"`
}

// Manifest is the canonical deterministic delivery manifest.
type Manifest struct {
	Version      string                 `json:"version"`
	Seed         int64                  `json:"seed"`
	Timescale    uint32                 `json:"timescale"`
	SegmentCount int                    `json:"segment_count"`
	Segments     []align.AlignedSegment `json:"segments"`
	Events       []metadata.Event       `json:"events"`
	IndexRebuilt int                    `json:"index_rebuilt"`
	Quarantined  []QuarantinedSegment   `json:"quarantined,omitempty"`
}

// Inputs groups optional data used to build a manifest.
type Inputs struct {
	Seed         int64
	Alignment    *align.Report
	Events       []metadata.Event
	MP4Reports   []*mp4.Report
	IndexRebuilt int
}

// New builds a manifest and canonicalizes/dedupes its timed events.
func New(in Inputs) *Manifest {
	m := &Manifest{
		Version:      Version,
		Seed:         in.Seed,
		Timescale:    1000,
		Events:       metadata.SortAndDedupe(in.Events),
		IndexRebuilt: in.IndexRebuilt,
	}
	if in.Alignment != nil {
		m.Timescale = in.Alignment.Timescale
		m.Segments = append([]align.AlignedSegment(nil), in.Alignment.Segments...)
	}
	m.SegmentCount = len(m.Segments)
	for _, rr := range in.MP4Reports {
		if rr == nil {
			continue
		}
		for _, s := range rr.Segments {
			if s.Corrupt || (!s.Readable && s.TimingKnown) {
				m.Quarantined = append(m.Quarantined, QuarantinedSegment{
					Representation:        rr.Representation,
					Sequence:              s.Sequence,
					BoxOffset:             s.BoxOffset,
					FirstUnreadableOffset: s.FirstUnreadableOffset,
				})
			}
		}
	}
	return m
}

// CanonicalJSON returns stable JSON with a trailing newline.
func CanonicalJSON(m *Manifest) ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// SHA256 returns the hex digest of data.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// WriteFile atomically writes canonical manifest JSON and returns its digest.
// Repeating the same input writes the same bytes and digest.
func WriteFile(path string, m *Manifest) (string, []byte, error) {
	data, err := CanonicalJSON(m)
	if err != nil {
		return "", nil, err
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", nil, err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", nil, err
	}
	return SHA256(data), data, nil
}
