// Package manifest renders the broadcast segment manifest and verifies
// a re-read manifest reproduces the exact same segment sequence. The
// on-disk form is deterministic JSON (fixed struct field order, sorted
// representations) so repeated runs over identical inputs are
// byte-for-byte stable and diff-friendly for operator review.
package manifest

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"

	"mediq/internal/segment"
	"mediq/internal/timeline"
)

// Version is the manifest schema version.
const Version = 1

// SegmentRecord is one row of the manifest.
type SegmentRecord struct {
	Boundary    int    `json:"boundary"`
	RepID       string `json:"representation"`
	Codec       string `json:"codec"`
	StartPTS90k int64  `json:"start_pts_90k"`
	EndPTS90k   int64  `json:"end_pts_90k"`
	StartOffset int64  `json:"start_offset"`
	Length      int64  `json:"length"`
	KeyframeSeq uint32 `json:"keyframe_seq"`
	IndexSource string `json:"index_source"`
	Quarantined bool   `json:"quarantined"`
	SHA256      string `json:"sha256"`
}

// Representation groups the rows of one bitrate layer.
type Representation struct {
	ID       string          `json:"id"`
	Codec    string          `json:"codec"`
	Segments []SegmentRecord `json:"segments"`
}

// Manifest is the complete broadcast-ready document.
type Manifest struct {
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   string            `json:"generated_at_utc"`
	TimescaleHz   int               `json:"timescale_hz"`
	ToleranceMs   int64             `json:"tolerance_ms"`
	SkewMaxMs     float64           `json:"cross_representation_skew_ms"`
	InputSHA256   map[string]string `json:"input_sha256"`
	Reps          []Representation  `json:"representations"`
}

// Options controls rendering.
type Options struct {
	Tolerance   timeline.Ticks
	SkewMaxMs   float64
	InputHash   map[string]string
	GeneratedAt time.Time
}

// Build assembles a manifest from aligned plans. Per-segment SHA-256
// values come from segHash keyed by representation ID and boundary.
func Build(plans []*segment.Plan, segHash map[string]map[int]string, opts Options) *Manifest {
	m := &Manifest{
		SchemaVersion: Version,
		GeneratedAt:   opts.GeneratedAt.UTC().Format(time.RFC3339),
		TimescaleHz:   timeline.Timescale,
		ToleranceMs:   opts.Tolerance.Millis(),
		SkewMaxMs:     opts.SkewMaxMs,
		InputSHA256:   sortedCopy(opts.InputHash),
	}
	ordered := append([]*segment.Plan(nil), plans...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].RepID < ordered[j].RepID })
	for _, p := range ordered {
		rep := Representation{ID: p.RepID, Codec: p.Codec.String()}
		for _, s := range p.Segments {
			rec := SegmentRecord{
				Boundary:    s.Index,
				RepID:       p.RepID,
				Codec:       s.Codec.String(),
				StartPTS90k: int64(s.StartPTS),
				EndPTS90k:   int64(s.EndPTS),
				StartOffset: s.StartOffset,
				Length:      s.Bytes,
				KeyframeSeq: s.KeyframeSeq,
				IndexSource: string(s.IndexSource),
				Quarantined: s.Quarantined,
			}
			if byRep, ok := segHash[p.RepID]; ok {
				rec.SHA256 = byRep[s.Index]
			}
			rep.Segments = append(rep.Segments, rec)
		}
		m.Reps = append(m.Reps, rep)
	}
	return m
}

// Marshal serializes the manifest with stable indentation and a
// trailing newline.
func Marshal(m *Manifest) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sortedCopy(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
