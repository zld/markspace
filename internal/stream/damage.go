package stream

import (
	"github.com/local/medialab/internal/container"
	"github.com/local/medialab/internal/model"
	"github.com/local/medialab/internal/timeline"
)

// DamageReport is the located defect summary for one stream.
type DamageReport struct {
	// Ranges are merged, start-ordered master-clock windows covering all
	// structural defects; these print as damage_ranges in the summary.
	Ranges          []timeline.Window
	Truncated       bool
	MissingIndex    bool
	InvalidTime     bool
	IncompleteFrame bool
}

// DetectDamage inspects normalized metadata and records every structural
// defect with an offset, track and time window. It never invents a success
// state: callers must treat a non-empty report as a damaged stream.
func DetectDamage(meta *container.MP4Meta, fileSize int64, stats FrameStats, book *model.ErrorBook) DamageReport {
	rep := DamageReport{}

	for _, tr := range meta.Tracks {
		if tr.Kind != "video" {
			continue
		}
		// Missing keyframe index: the whole track's keyframe claims are
		// untrustworthy, but the window is bounded by track duration.
		if len(tr.KeyframeSampleNumbers) == 0 {
			rep.MissingIndex = true
			book.Add(model.ErrorRecord{
				Code: model.CodeMissingIndex, Track: tr.ID,
				Offset: 0,
				Window: timeline.Window{Start: 0, End: tr.DurationTicks},
				Detail: "video track has no keyframe index (stss absent)",
			})
		}
	}

	// Incomplete frames were recorded during analysis; mirror the state.
	if stats.IncompleteCount > 0 {
		rep.IncompleteFrame = true
		rep.Truncated = true
	}

	// A declared sample region beyond EOF is a truncation window even if
	// the box parser tolerated it.
	for _, s := range meta.Samples {
		if s.Offset+int64(s.Size) > fileSize {
			rep.Truncated = true
			end := s.TimeTick + timeline.MasterHz/30
			book.Add(model.ErrorRecord{
				Code: model.CodeTruncated, Offset: s.Offset, Track: s.Track,
				Window: timeline.Window{Start: s.TimeTick, End: end},
				Detail: "declared sample lies beyond end of file",
			})
		}
	}

	// Invalid timestamps: non-positive presentation deltas.
	detectInvalidTimestamps(meta, book, &rep)

	rep.Ranges = book.MergeWindows(
		model.CodeTruncated, model.CodeIncompleteFrame,
		model.CodeInvalidTimestamp, model.CodeMissingIndex,
	)
	return rep
}

// detectInvalidTimestamps walks per-track presentation times and flags
// zero/negative deltas with the offending sample's offset and a
// reviewable one-frame-wide window.
func detectInvalidTimestamps(meta *container.MP4Meta, book *model.ErrorBook, rep *DamageReport) {
	lastByTrack := map[uint32]int64{}
	seen := map[uint32]bool{}
	for _, s := range meta.Samples {
		if seen[s.Track] && s.TimeTick <= lastByTrack[s.Track] {
			rep.InvalidTime = true
			book.Add(model.ErrorRecord{
				Code: model.CodeInvalidTimestamp, Offset: s.Offset, Track: s.Track,
				Window: timeline.Window{
					Start: s.TimeTick,
					End:   s.TimeTick + timeline.MasterHz/30,
				},
				Detail: "non-positive presentation timestamp delta",
			})
		}
		lastByTrack[s.Track] = s.TimeTick
		seen[s.Track] = true
	}
}

// AVSync measures audio/video alignment in milliseconds. It returns the
// largest absolute presentation-time difference between the first audio
// and video sample and any common-index samples; a healthy stream stays
// within 20ms.
func AVSync(meta *container.MP4Meta) int64 {
	var firstVideo, firstAudio int64
	haveV, haveA := false, false
	for _, s := range meta.Samples {
		if s.Track == 1 && !haveV {
			firstVideo, haveV = s.TimeTick, true
		}
		if s.Track == 2 && !haveA {
			firstAudio, haveA = s.TimeTick, true
		}
	}
	if !haveV || !haveA {
		return 0
	}
	diff := firstVideo - firstAudio
	if diff < 0 {
		diff = -diff
	}
	// Compare periodic alignment at every video frame boundary.
	video := map[int64]bool{}
	for _, s := range meta.Samples {
		if s.Track == 1 {
			video[s.TimeTick] = true
		}
	}
	var worst int64 = diff
	for _, s := range meta.Samples {
		if s.Track != 2 {
			continue
		}
		if video[s.TimeTick] {
			continue // perfectly aligned common tick
		}
		// nearest video tick distance within one frame
		d := nearestDistance(s.TimeTick, video)
		if d > worst {
			worst = d
		}
	}
	return timeline.Millis(worst)
}

func nearestDistance(tick int64, set map[int64]bool) int64 {
	best := int64(1 << 62)
	for k := range set {
		d := tick - k
		if d < 0 {
			d = -d
		}
		if d < best {
			best = d
		}
	}
	return best
}
