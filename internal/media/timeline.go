package media

import "fmt"

// SampleKind classifies timeline points.
type SampleKind uint8

const (
	KindUnknown SampleKind = iota
	KindVideo
	KindAudio
	KindHint
)

// BoundarySource records whether a timeline boundary comes from a decoded
// table or from an inference made because the index was missing/truncated.
type BoundarySource uint8

const (
	// SourceDecoded: the boundary is backed by a complete sample table.
	SourceDecoded BoundarySource = iota
	// SourceInferred: the boundary is a guess (e.g. truncated stts); it must
	// never be reported as a fully decodable keyframe boundary.
	SourceInferred
)

// Point is one sample boundary on the unified nanosecond timeline. VFR
// streams keep their real per-sample durations here; nothing downstream
// rewrites these onto an average frame rate.
type Point struct {
	StartNanos   int64          `json:"start_nanos"`
	DurationNanos int64         `json:"duration_nanos"`
	IsSync       bool           `json:"is_sync"`
	Kind         SampleKind     `json:"kind"`
	Source       BoundarySource `json:"source"`
	// MediaTick is the unwrapped media-time tick of the point, kept for
	// damage localization back onto track clocks.
	MediaTick int64 `json:"media_tick"`
}

// Edit is one elst segment expressed on nanosecond clocks. Negative
// MediaStartNanos marks an empty (delay) edit.
type Edit struct {
	MovieStartNanos  int64 `json:"movie_start_nanos"`
	MovieDurationNanos int64 `json:"movie_duration_nanos"`
	MediaStartNanos  int64 `json:"media_start_nanos"`
}

// IsEmpty reports an empty edit whose media time is undefined.
func (e Edit) IsEmpty() bool { return e.MediaStartNanos < 0 }

// TrackLine is one track's explicit timeline. Undecidable=true means the
// container index was incomplete: the tail points are inferences and the
// track must not be reported as fully indexed.
type TrackLine struct {
	TrackID     uint32    `json:"track_id"`
	Kind        SampleKind `json:"kind"`
	TimeBase    TimeBase  `json:"-"`
	Points      []Point   `json:"points"`
	Edits       []Edit    `json:"edits,omitempty"`
	Undecidable bool      `json:"undecidable"`
	// Monotonic is verified after wrap expansion and edit mapping.
	Monotonic bool `json:"monotonic"`
}

// FrameGap is one observed inter-frame interval on a track, in nanoseconds.
type FrameGap struct {
	TrackID       uint32 `json:"track_id"`
	Index         int    `json:"index"`
	GapNanos      int64  `json:"gap_nanos"`
	Deterministic bool   `json:"deterministic"`
}

// Gaps returns the actual VFR-preserving intervals between consecutive
// points. Inferred boundaries are flagged so consumers never mistake them
// for measured cadence.
func (l *TrackLine) Gaps() []FrameGap {
	if len(l.Points) < 2 {
		return nil
	}
	out := make([]FrameGap, 0, len(l.Points)-1)
	for i := 1; i < len(l.Points); i++ {
		out = append(out, FrameGap{
			TrackID:       l.TrackID,
			Index:         i,
			GapNanos:      l.Points[i].StartNanos - l.Points[i-1].StartNanos,
			Deterministic: l.Points[i].Source == SourceDecoded &&
				l.Points[i-1].Source == SourceDecoded,
		})
	}
	return out
}

// CheckMonotonic verifies non-decreasing start times and strictly positive
// durations, recording the result on the line.
func (l *TrackLine) CheckMonotonic() bool {
	ok := true
	for i, p := range l.Points {
		if p.DurationNanos <= 0 {
			ok = false
		}
		if i > 0 && p.StartNanos < l.Points[i-1].StartNanos {
			ok = false
		}
	}
	l.Monotonic = ok
	return ok
}

// RawSample is one pre-timeline entry straight from the sample tables:
// unwrapped media tick plus duration in track ticks.
type RawSample struct {
	MediaTick    int64
	DurationTick uint32
	IsSync       bool
	Source       BoundarySource
}

// BuildTrackLine expands raw samples onto the unified nanosecond timeline,
// then applies the edit list when present. The input order is preserved;
// wrap expansion happens before nanosecond conversion so wrapped streams
// still produce a monotonic line.
func BuildTrackLine(trackID uint32, kind SampleKind, tb TimeBase, raw []RawSample, edits []Edit, undecidable bool) (*TrackLine, error) {
	line := &TrackLine{TrackID: trackID, Kind: kind, TimeBase: tb, Undecidable: undecidable}
	pts := make([]Point, 0, len(raw))
	for _, s := range raw {
		pts = append(pts, Point{
			StartNanos:    tb.ToNanos(s.MediaTick),
			DurationNanos: tb.ToNanos(int64(s.DurationTick)),
			IsSync:        s.IsSync,
			Kind:          kind,
			Source:        s.Source,
			MediaTick:     s.MediaTick,
		})
	}
	if len(edits) > 0 {
		mapped, err := applyEdits(pts, edits)
		if err != nil {
			return nil, err
		}
		line.Points = mapped
		line.Edits = edits
	} else {
		line.Points = pts
	}
	line.CheckMonotonic()
	return line, nil
}

// applyEdits slices the media-time point list into elst segments and lays
// the segments back-to-back on the movie timeline. Empty edits contribute
// timeline gaps without points.
func applyEdits(pts []Point, edits []Edit) ([]Point, error) {
	out := make([]Point, 0, len(pts))
	var cursor int64
	for _, ed := range edits {
		if ed.IsEmpty() {
			cursor += ed.MovieDurationNanos
			continue
		}
		segStart := ed.MediaStartNanos
		segEnd := ed.MediaStartNanos + ed.MovieDurationNanos
		var shift int64
		placed := false
		for i := range pts {
			p := pts[i]
			if p.StartNanos < segStart || p.StartNanos >= segEnd {
				continue
			}
			if !placed {
				shift = cursor - p.StartNanos
				placed = true
			}
			p.StartNanos += shift
			out = append(out, p)
		}
		if !placed {
			return nil, fmt.Errorf("edit segment [%d,%d) matches no samples", segStart, segEnd)
		}
		cursor += ed.MovieDurationNanos
	}
	return out, nil
}
