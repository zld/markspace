package model

import "mediaforensics/internal/clock"

// Media kinds used by hdlr handler_type in the synthetic MP4 subset.
const (
	KindVideo = "vide"
	KindAudio = "soun"
)

// Sample is one contiguous media sample (frame or audio packet) located in
// the input container. Every sample remembers its source byte range so that
// rendered artifacts can be traced to the bytes that produced them.
type Sample struct {
	TrackID    int            `json:"track_id"`
	Index      int            `json:"index"`
	Offset     int64          `json:"offset"`
	Size       int64          `json:"size"`
	Start      clock.Rational `json:"start"` // seconds on the unified timeline
	Duration   clock.Rational `json:"duration"`
	Keyframe   bool           `json:"keyframe"`
	Corrupt    bool           `json:"corrupt"`
	Confidence string         `json:"confidence"`
	Basis      string         `json:"basis"` // why the confidence grade applies
}

// End returns Start+Duration; overflow is the caller's concern via Add.
func (s Sample) End() (clock.Rational, error) {
	return s.Start.Add(s.Duration)
}

// Clip is an edit-list style reference that places samples onto a track.
type Clip struct {
	TrackID     int            `json:"track_id"`
	FromSample  int            `json:"from_sample"`
	ToSample    int            `json:"to_sample"` // exclusive
	TrackStart  clock.Rational `json:"track_start"`
	TrackOffset clock.Rational `json:"track_offset"` // non-zero composition offset
	ZeroLength  bool           `json:"zero_length"`
}

// Track groups samples and the clips that schedule them.
type Track struct {
	ID            int      `json:"id"`
	Kind          string   `json:"kind"`
	Timescale     int64    `json:"timescale"`
	EarliestOffset int64   `json:"earliest_offset"` // first source offset, kept on overflow
	Samples       []Sample `json:"samples"`
	Clips         []Clip   `json:"clips"`
	Blocked       bool     `json:"blocked"`     // overflow / fatal defect
	BlockReason   string   `json:"block_reason"`
}

// ValidSamples returns the samples allowed onto the timeline: not corrupt and
// at least medium confidence.
func (t *Track) ValidSamples() []Sample {
	out := make([]Sample, 0, len(t.Samples))
	for _, s := range t.Samples {
		if s.Corrupt || s.Confidence == ConfidenceNone || s.Confidence == ConfidenceLow {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Timeline is the unified, clock-normalized representation consumed by the
// RTP, render and validate subsystems.
type Timeline struct {
	Tracks       []*Track       `json:"tracks"`
	Errors       []*ForensicError `json:"errors"`
	VideoTracks  []int          `json:"video_tracks"`
	AudioTracks  []int          `json:"audio_tracks"`
	Duration     clock.Rational `json:"duration"`
}

// Track finds a track by ID.
func (tl *Timeline) Track(id int) *Track {
	for _, tr := range tl.Tracks {
		if tr.ID == id {
			return tr
		}
	}
	return nil
}

// AddError appends a forensic error, preserving insertion order so repeated
// runs produce identical error summaries.
func (tl *Timeline) AddError(e *ForensicError) {
	if e != nil {
		tl.Errors = append(tl.Errors, e)
	}
}

// ScheduledSample is a sample placed at an absolute unified-timeline time.
type ScheduledSample struct {
	Sample Sample           `json:"sample"`
	At     clock.Rational   `json:"at"`
	Clip   Clip             `json:"clip"`
}

// Interval marks a usable sample range rebuilt by the index layer.
type Interval struct {
	TrackID    int    `json:"track_id"`
	FirstSample int   `json:"first_sample"`
	LastSample  int   `json:"last_sample"` // inclusive
	Confidence string `json:"confidence"`
	Basis      string `json:"basis"`
}
