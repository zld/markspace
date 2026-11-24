// Package manifest parses HLS media playlists and DASH MPD documents and
// maps every listed segment onto an ordered, time-ranged view. Sequence
// gaps, duplicate numbers and discontinuity markers are reported with the
// offset inside the source document instead of being silently joined.
package manifest

import (
	"mediascan/internal/fault"
)

// Format names the playlist dialect.
type Format string

const (
	FormatHLS  Format = "hls"
	FormatDASH Format = "dash"
)

// Segment is one playable media chunk in playlist order.
type Segment struct {
	Index    int     `json:"index"`
	MediaSeq uint64  `json:"media_seq"`
	URI      string  `json:"uri"`
	Duration float64 `json:"duration_seconds"`
	Start    float64 `json:"start_seconds"`
	End      float64 `json:"end_seconds"`
	// SourceOffset is the line/byte offset of the segment entry in the
	// originating document.
	SourceOffset int64 `json:"source_offset"`
	// Discontinuity marks an explicit boundary before this segment.
	Discontinuity bool `json:"discontinuity"`
}

// Manifest is the normalized result for either dialect.
type Manifest struct {
	Format   Format    `json:"format"`
	Segments []Segment `json:"segments"`
	// TargetDuration is the declared nominal segment duration (seconds).
	TargetDuration float64 `json:"target_duration"`
	// StartSeq is the declared first media sequence number.
	StartSeq uint64 `json:"start_seq"`
	// TimeScale maps DASH's timescale into seconds (HLS uses 1).
	TimeScale uint32 `json:"time_scale"`
}

// TimeRange returns the playlist's covered window in seconds.
func (m *Manifest) TimeRange() (float64, float64) {
	if len(m.Segments) == 0 {
		return 0, 0
	}
	return m.Segments[0].Start, m.Segments[len(m.Segments)-1].End
}

// parser is shared by both dialect implementations.
type parser struct {
	log *fault.Log
}

func newParser() parser { return parser{log: fault.NewLog()} }

// checkSequences enforces gap/duplicate rules against ordered segments.
func (p *parser) checkSequences(segs []Segment) {
	var prev uint64
	for i, s := range segs {
		if i == 0 {
			prev = s.MediaSeq
			continue
		}
		switch {
		case s.MediaSeq == prev:
			p.log.Add(fault.Record{
				Stage: fault.StageManifest, Severity: fault.SeverityError,
				Kind: fault.KindDuplicateSequence, Segment: s.URI,
				MediaSeq: s.MediaSeq, SourceOffset: s.SourceOffset,
				Message: "duplicate media sequence number",
			})
		case s.MediaSeq > prev+1:
			p.log.Add(fault.Record{
				Stage: fault.StageManifest, Severity: fault.SeverityError,
				Kind: fault.KindMissingSegment, Segment: s.URI,
				MediaSeq: s.MediaSeq, SourceOffset: s.SourceOffset,
				Message: "sequence gap: missing segment(s) before this entry",
			})
		}
		prev = s.MediaSeq
	}
}
