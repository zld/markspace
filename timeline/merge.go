// Package timeline also owns the merge of per-track block timelines onto the
// common nanosecond clock. The merge input uses local, dependency-free structs
// so the clock package stays at the bottom of the dependency graph; the
// pipeline layer maps EBML scan results into these structs.
package timeline

import "sort"

// BlockInput is one decoded block in merge-input form.
type BlockInput struct {
	TrackNumber uint32
	TimeNs      int64
	Offset      int64
	Size        int64
	CheckOK     bool
}

// TrackInput is the immutable description merge needs for one track.
type TrackInput struct {
	Number     uint32
	UID        uint32
	IsVideo    bool
	SampleRate int64 // audio
	FrameRate  int64 // video ticks per second
}

// Event is one point on the merged programme timeline.
type Event struct {
	TrackNumber uint32
	TimeNs      int64
	Offset      int64
	Size        int64
	CheckOK     bool
	Missing     bool // synthesised placeholder for a detected gap
	WrapCount   int  // rollover periods applied to recover the timestamp
}

// Gap marks a continuity break on one track and the timeline interval it spans.
type Gap struct {
	TrackNumber uint32
	StartNs     int64
	EndNs       int64
	Offset      int64
	Reason      string
}

// TrackTimeline is one track's ordered events plus its recovered rollovers.
type TrackTimeline struct {
	TrackNumber uint32
	UID         uint32
	IsVideo     bool
	Rate        int64
	Events      []*Event
	Gaps        []Gap
}

// MergeResult is the combined, clock-aligned view of every track.
type MergeResult struct {
	Events       []*Event // globally time-ordered
	Tracks       map[uint32]*TrackTimeline
	Trusted      bool
	RejectReason string
}

// MergeConfig controls rollover recovery and gap synthesis.
type MergeConfig struct {
	// RolloverTicks is the per-track period at which raw timecodes wrap.
	RolloverTicks int64
	// MaxStepTicks is the largest plausible absolute step between two
	// consecutive blocks; rollovers recovering a bigger step are ambiguous.
	MaxStepTicks int64
	// MaxMissingIntervals bounds synthesised placeholders per track.
	MaxMissingIntervals int
}

// DefaultMergeConfig returns the standard fixture limits.
func DefaultMergeConfig() MergeConfig {
	return MergeConfig{RolloverTicks: 1 << 32, MaxStepTicks: 8, MaxMissingIntervals: 64}
}

// Merge rebuilds per-track timelines, recovers timecode rollovers from
// per-track continuity, synthesises missing-block placeholders and merges
// everything onto the common nanosecond clock. When a rollover cannot be
// uniquely recovered Trusted becomes false and callers must stop remuxing.
func Merge(blocks []BlockInput, tracks []TrackInput, cfg MergeConfig) (*MergeResult, error) {
	byTrack := make(map[uint32][]BlockInput)
	for _, b := range blocks {
		byTrack[b.TrackNumber] = append(byTrack[b.TrackNumber], b)
	}
	res := &MergeResult{Tracks: make(map[uint32]*TrackTimeline), Trusted: true}
	for _, spec := range tracks {
		raw := byTrack[spec.Number]
		if len(raw) == 0 {
			res.Tracks[spec.Number] = &TrackTimeline{
				TrackNumber: spec.Number, UID: spec.UID, IsVideo: spec.IsVideo,
				Gaps: []Gap{{TrackNumber: spec.Number, Reason: "track has no blocks"}},
			}
			res.Trusted = false
			res.RejectReason = "track with no blocks cannot be reconstructed"
			continue
		}
		tl, trusted, reason, err := buildTrack(spec, raw, cfg)
		if err != nil {
			return nil, err
		}
		res.Tracks[spec.Number] = tl
		if !trusted {
			res.Trusted = false
			res.RejectReason = reason
		}
	}
	for _, tl := range res.Tracks {
		res.Events = append(res.Events, tl.Events...)
	}
	sort.SliceStable(res.Events, func(i, j int) bool {
		if res.Events[i].TimeNs != res.Events[j].TimeNs {
			return res.Events[i].TimeNs < res.Events[j].TimeNs
		}
		return res.Events[i].TrackNumber < res.Events[j].TrackNumber
	})
	return res, nil
}
