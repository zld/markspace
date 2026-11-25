package kernel

import (
	"fmt"
	"sort"
)

// SegmentTrack contains the samples selected for one segment and track.
type SegmentTrack struct {
	TrackID string    `json:"track_id"`
	PID     uint16    `json:"pid"`
	Kind    TrackKind `json:"kind"`
	Samples []Sample  `json:"samples"`
}

// Segment is a deterministic half-open event-aligned deliverable plan.
type Segment struct {
	Index       int               `json:"index"`
	ID          string            `json:"id"`
	File        string            `json:"file"`
	Time        TimeRange         `json:"time"`
	EventIDs    []string          `json:"event_ids,omitempty"`
	GapIDs      []string          `json:"gap_ids,omitempty"`
	Tracks      []SegmentTrack    `json:"tracks"`
	Silence     []SilenceInterval `json:"silence,omitempty"`
	ContentHash string            `json:"content_hash,omitempty"`
	FileSHA256  string            `json:"file_sha256,omitempty"`
	SizeBytes   int64             `json:"size_bytes,omitempty"`
}

// CutConfig controls event slicing and shared silence policy.
type CutConfig struct {
	Boundaries90k []uint64
	Silence       SilenceConfig
	PSI           *PSITracker
}

// CutProgram slices all normalized tracks on one shared clock.
func CutProgram(prog *NormalizedProgram, cfg CutConfig) ([]*Segment, error) {
	if prog == nil {
		return nil, &MediaError{Stage: "cut", Code: "nil_program", Message: "program is required", Recoverable: false}
	}
	if cfg.PSI != nil && cfg.PSI.Incomplete() {
		return nil, &MediaError{
			Stage:       "cut",
			Code:        "psi_version_incomplete",
			Message:     fmt.Sprintf("PMT update incomplete; paused PIDs %v", cfg.PSI.PausedPIDList()),
			PID:         0x1000,
			Time:        TimeRange{Start90k: 0, End90k: prog.Duration90k},
			Recoverable: true,
		}
	}

	bounds := []uint64{0}
	bounds = append(bounds, cfg.Boundaries90k...)
	for _, ev := range prog.Events {
		if ev.Label == "cut" {
			bounds = append(bounds, ev.PTS90k)
		}
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
	unique := bounds[:0]
	var last uint64
	for i, b := range bounds {
		if i == 0 || b != last {
			unique = append(unique, b)
		}
		last = b
	}
	if len(unique) == 0 || unique[0] != 0 {
		unique = append([]uint64{0}, unique...)
	}
	if prog.Duration90k == 0 {
		return nil, &MediaError{Stage: "cut", Code: "empty_program", Message: "duration is zero", Recoverable: false}
	}
	if unique[len(unique)-1] < prog.Duration90k {
		unique = append(unique, prog.Duration90k)
	}

	audioSilence := map[string][]SilenceInterval{}
	for _, tr := range prog.Tracks {
		if tr.Kind == TrackAudio {
			intervals, err := DetectSilence(tr, prog.Gaps, cfg.Silence)
			if err != nil {
				return nil, err
			}
			audioSilence[tr.ID] = intervals
		}
	}

	var segments []*Segment
	for i := 0; i+1 < len(unique); i++ {
		r := TimeRange{Start90k: unique[i], End90k: unique[i+1]}
		if r.Duration90k() == 0 {
			continue
		}
		if r.End90k > prog.Duration90k {
			return nil, &MediaError{
				Stage:       "cut",
				Code:        "boundary_out_of_range",
				Message:     fmt.Sprintf("boundary %d exceeds duration %d", r.End90k, prog.Duration90k),
				Time:        r,
				Recoverable: false,
			}
		}
		seg := &Segment{
			Index: i + 1,
			ID:    fmt.Sprintf("segment-%03d", i+1),
			File:  fmt.Sprintf("segments/segment-%03d.mp4", i+1),
			Time:  r,
		}
		for _, tr := range prog.Tracks {
			if cfg.PSI != nil && !cfg.PSI.CuttingEnabled(tr.PID) {
				return nil, &MediaError{
					Stage:       "cut",
					Code:        "track_mapping_paused",
					Message:     fmt.Sprintf("track %s PID 0x%04x has no complete PSI map", tr.ID, tr.PID),
					PID:         tr.PID,
					Time:        r,
					Recoverable: true,
				}
			}
			st := SegmentTrack{TrackID: tr.ID, PID: tr.PID, Kind: tr.Kind}
			for _, s := range tr.Samples {
				if s.PTS90k >= r.Start90k && s.PTS90k < r.End90k {
					st.Samples = append(st.Samples, s)
				}
			}
			seg.Tracks = append(seg.Tracks, st)

			for _, in := range audioSilence[tr.ID] {
				if piece, ok := in.Time.Intersect(r); ok {
					clipped := in
					clipped.Time = piece
					if piece.DurationMS() >= cfg.Silence.MinDurationMS {
						seg.Silence = append(seg.Silence, clipped)
					}
				}
			}
		}
		for _, ev := range prog.Events {
			if r.Contains(ev.PTS90k) {
				seg.EventIDs = append(seg.EventIDs, ev.ID)
			}
		}
		for _, g := range prog.Gaps {
			if _, ok := g.Time.Intersect(r); ok {
				seg.GapIDs = append(seg.GapIDs, g.ID)
			}
		}
		segments = append(segments, seg)
	}
	if len(segments) == 0 {
		return nil, &MediaError{Stage: "cut", Code: "no_segments", Message: "event range produced no segments", Recoverable: false}
	}
	return segments, nil
}
