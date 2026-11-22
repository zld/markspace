package adapter

import (
	"mediacore/media"
)

// Kind distinguishes the media carried by a track.
type Kind uint8

const (
	KindUnknown Kind = iota
	KindAudio
	KindVideo
)

// Sample is one decoded sample-table entry, located on the unified timeline
// after edit-list mapping has been applied.
type Sample struct {
	Index       uint32 // 0-based sample index inside its track
	ByteOffset  int64  // absolute file offset of the sample bytes
	Size        uint32
	PTS         int64 // presentation time, unified nanoseconds
	Duration    int64 // unified nanoseconds, always > 0
	IsKeyframe  bool
}

// Edit is one mapped edit-list segment on the unified timeline.
type Edit struct {
	TrackID   uint32
	Segment   media.Interval // presentation interval on the unified axis
	MediaTime int64          // media-time start in track ticks, -1 = empty edit
}

// TrackManifest is the parser output for a single track.
type TrackManifest struct {
	TrackID   uint32
	Kind      Kind
	Timescale media.Rational
	// StartPTS is the non-zero presentation origin carried by the source,
	// preserved (not normalised away) for audit.
	StartPTS  int64
	Duration  int64 // unified nanoseconds
	Samples   []Sample
	Edits     []Edit
	Channels  uint16 // audio only
	Rate      uint32 // audio sample rate or video frame rate hint
}

// Manifest is the parser output for a whole container or stream bundle.
type Manifest struct {
	Source      string
	AdapterName string
	Version     uint32
	Tracks      []*TrackManifest
	ParseBytes  int
}

// Validate runs the cross-track structural checks shared by every adapter:
// monotonic PTS, positive durations and the global kernel limits.
func (m *Manifest) Validate() error {
	if err := EnforceLimits(len(m.Tracks), m.durationNanos(), m.ParseBytes); err != nil {
		return err
	}
	for _, tr := range m.Tracks {
		var prevEnd int64
		for _, s := range tr.Samples {
			if s.Duration <= 0 {
				return media.Errorf(media.CodeNegativeDuration, tr.TrackID,
					&media.Interval{Start: s.PTS, End: s.PTS + s.Duration},
					s.ByteOffset, "sample %d has non-positive duration", s.Index)
			}
			if s.PTS < prevEnd && s.Index > 0 {
				return media.Errorf(media.CodePTSRollback, tr.TrackID,
					&media.Interval{Start: s.PTS, End: s.PTS + s.Duration},
					s.ByteOffset, "sample %d PTS rolls back", s.Index)
			}
			prevEnd = s.PTS + s.Duration
		}
	}
	return nil
}

func (m *Manifest) durationNanos() int64 {
	var max int64
	for _, tr := range m.Tracks {
		if tr.Duration > max {
			max = tr.Duration
		}
	}
	return max
}
