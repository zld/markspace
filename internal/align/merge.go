// Package align performs deterministic, repeatable merging of fragment
// references across DASH representations and RTP streams. Sorting is total
// (time, representation rank, fragment index) so identical inputs always
// produce identical output; cross-track synchronization is measured on the
// shared master clock and any failed stage aborts before a manifest is
// committed.
package align

import (
	"sort"

	"mediakernel/internal/clock"
	"mediakernel/internal/mediaerr"
)

// Fragment is one alignable unit carrying its position on every relevant
// timeline in master ticks.
type Fragment struct {
	Source       string `json:"source"`        // dash | hls | rtp
	TrackID      string `json:"track_id"`      // representation / stream id
	Kind         string `json:"kind"`          // video | audio
	Rank         int    `json:"rank"`          // representation ordering number
	SegmentIndex uint32 `json:"segment_index"` // fragment ordinal
	StartTick    uint64 `json:"start_tick"`    // master 90 kHz
	DurationTick uint64 `json:"duration_tick"`
	EndTick      uint64 `json:"end_tick"`
	Sequence     uint64 `json:"sequence,omitempty"` // RTP/HLS source sequence
}

// SyncPair is one video/audio alignment observation.
type SyncPair struct {
	VideoTick   uint64 `json:"video_tick"`
	AudioTick   uint64 `json:"audio_tick"`
	ErrorTick   uint64 `json:"error_tick"` // absolute difference on master clock
	ErrorMillis uint64 `json:"error_ms"`
}

// Gap is a missing time range on one track.
type Gap struct {
	TrackID     string `json:"track_id"`
	StartTick   uint64 `json:"start_tick"`
	EndTick     uint64 `json:"end_tick"`
	Recoverable bool   `json:"recoverable"`
}

// Result is the deterministic merge output.
type Result struct {
	Fragments   []Fragment `json:"fragments"`
	Sync        []SyncPair `json:"sync"`
	Gaps        []Gap      `json:"gaps"`
	MaxSyncTick uint64     `json:"max_sync_tick"`
}

// Options controls alignment tolerance.
type Options struct {
	SyncBudgetTick uint64 // allowed video/audio skew; beyond it is an error
}

// DefaultOptions returns a 20 ms A/V sync budget (the acceptance target).
func DefaultOptions() Options {
	budget, _ := clock.FromMillis(20)
	return Options{SyncBudgetTick: budget}
}

// Merge sorts fragments with a total deterministic ordering and returns the
// merged timeline. The input slice is not modified.
func Merge(in []Fragment) ([]Fragment, error) {
	if len(in) == 0 {
		return nil, mediaerr.New(mediaerr.CatAlignment, "empty_merge", "no fragments to merge")
	}
	if len(in) > mediaerr.MaxSegments*mediaerr.MaxRepresentations {
		return nil, mediaerr.New(mediaerr.CatValidation, "merge_cap",
			"merge input exceeds fragment limit").Wrap(mediaerr.ErrLimit)
	}
	out := append([]Fragment(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.StartTick != b.StartTick {
			return a.StartTick < b.StartTick
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if a.SegmentIndex != b.SegmentIndex {
			return a.SegmentIndex < b.SegmentIndex
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		return a.Sequence < b.Sequence
	})
	// Reject the same (track, segment) appearing twice with different spans.
	seen := map[string]Fragment{}
	for _, f := range out {
		key := f.TrackID + "/" + itoa32(f.SegmentIndex)
		if prev, ok := seen[key]; ok && (prev.StartTick != f.StartTick || prev.DurationTick != f.DurationTick) {
			return nil, mediaerr.New(mediaerr.CatAlignment, "conflicting_fragment",
				"same fragment referenced with conflicting time spans").
				With(f.TrackID, f.StartTick, false)
		}
		seen[key] = f
	}
	return out, nil
}

// Align matches the reference video track (lowest rank) against each audio
// fragment by fragment ordinal, since equal ordinals denote the same program
// moment even when the native start ticks differ slightly. The skew is
// measured on the master clock. Higher-rank video representations are treated
// as alternative encodings of the same moments, not separate pairing targets.
// Missing either side is recorded as a gap; skew beyond the budget fails.
func Align(frags []Fragment, opts Options) (*Result, error) {
	merged, err := Merge(frags)
	if err != nil {
		return nil, err
	}
	res := &Result{Fragments: merged}

	video := byKind(merged, "video")
	audio := byKind(merged, "audio")
	ref := referenceVideo(video)
	if ref == nil {
		return nil, mediaerr.New(mediaerr.CatAlignment, "no_video",
			"alignment requires a video reference track")
	}
	videoByIdx := indexByOrdinal(ref)
	audioTracks := groupByTrack(audio)

	for trackID, track := range audioTracks {
		audioByIdx := indexByOrdinal(track)
		for idx, v := range videoByIdx {
			a, ok := audioByIdx[idx]
			if !ok {
				res.Gaps = append(res.Gaps, Gap{
					TrackID: trackID, StartTick: v.StartTick,
					EndTick: v.EndTick, Recoverable: false,
				})
				continue
			}
			diff := clock.AbsDiff(v.StartTick, a.StartTick)
			ms, err := clock.ToMillis(diff)
			if err != nil {
				return nil, mediaerr.New(mediaerr.CatTimebase, "sync_overflow", err.Error())
			}
			res.Sync = append(res.Sync, SyncPair{
				VideoTick: v.StartTick, AudioTick: a.StartTick,
				ErrorTick: diff, ErrorMillis: ms,
			})
			if diff > res.MaxSyncTick {
				res.MaxSyncTick = diff
			}
			if diff > opts.SyncBudgetTick {
				return nil, mediaerr.New(mediaerr.CatAlignment, "sync_budget",
					"audio/video skew exceeds budget").
					With(trackID, a.StartTick, false)
			}
		}
		// Audio ordinals with no reference video slot are video-side gaps.
		for idx, a := range audioByIdx {
			if _, ok := videoByIdx[idx]; !ok {
				res.Gaps = append(res.Gaps, Gap{
					TrackID: trackID, StartTick: a.StartTick,
					EndTick: a.EndTick, Recoverable: false,
				})
			}
		}
	}
	// Gaps are collected from maps; sort them for deterministic output.
	sort.SliceStable(res.Gaps, func(i, j int) bool {
		if res.Gaps[i].StartTick != res.Gaps[j].StartTick {
			return res.Gaps[i].StartTick < res.Gaps[j].StartTick
		}
		return res.Gaps[i].TrackID < res.Gaps[j].TrackID
	})
	sort.SliceStable(res.Sync, func(i, j int) bool {
		if res.Sync[i].VideoTick != res.Sync[j].VideoTick {
			return res.Sync[i].VideoTick < res.Sync[j].VideoTick
		}
		return res.Sync[i].AudioTick < res.Sync[j].AudioTick
	})
	return res, nil
}

// referenceVideo returns the lowest-rank video track's fragments.
func referenceVideo(video []Fragment) []Fragment {
	var ref []Fragment
	bestRank := 0
	for i, f := range video {
		if i == 0 || f.Rank < bestRank {
			bestRank = f.Rank
		}
	}
	for _, f := range video {
		if f.Rank == bestRank {
			ref = append(ref, f)
		}
	}
	return ref
}

func indexByOrdinal(in []Fragment) map[uint32]Fragment {
	m := map[uint32]Fragment{}
	for _, f := range in {
		m[f.SegmentIndex] = f
	}
	return m
}

func groupByTrack(in []Fragment) map[string][]Fragment {
	m := map[string][]Fragment{}
	for _, f := range in {
		m[f.TrackID] = append(m[f.TrackID], f)
	}
	return m
}

func byKind(in []Fragment, kind string) []Fragment {
	var out []Fragment
	for _, f := range in {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func itoa32(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
