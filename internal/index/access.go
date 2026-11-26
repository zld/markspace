// Package index builds a random-access index over the repaired timeline. Every
// entry records the keyframe, its track time, the sample byte offset and the
// original (pre-repair) timeline mapping, so a seek target resolves to a
// decodable keyframe and the exact bytes that produced it.
package index

import (
	"encoding/json"
	"fmt"
	"sort"

	"rtx/internal/model"
	"rtx/internal/repair"
)

// Code values.
const (
	// CodeNoKeyframe marks a seek target that has no preceding keyframe.
	CodeNoKeyframe model.Code = "no_keyframe"
	// CodeIndexInvalid marks a structurally inconsistent index.
	CodeIndexInvalid model.Code = "index_invalid"
)

// Entry is one sample in the index.
type Entry struct {
	Source             string `json:"source"`
	Fragment           string `json:"fragment"`
	Sequence           uint32 `json:"sequence"`
	HasSequence        bool   `json:"has_sequence"`
	TrackID            uint32 `json:"track_id"`
	IsKeyframe         bool   `json:"is_keyframe"`
	TrackTicks         int64  `json:"track_ticks"`
	TrackRate          string `json:"track_rate"`
	MediaTicks         int64  `json:"media_ticks"`
	OriginalMediaTicks int64  `json:"original_media_ticks"`
	ByteOffset         int64  `json:"byte_offset"`
	Size               uint32 `json:"size"`
	FragmentByteOffset int64  `json:"fragment_byte_offset"`
	Duration           uint32 `json:"duration"`
}

// Index is the full random-access index. Media rate is stored as an explicit
// numerator/denominator so it survives JSON without float error.
type Index struct {
	MediaNum  int64   `json:"media_num"`
	MediaDen  int64   `json:"media_den"`
	Entries   []Entry `json:"entries"`
	Keyframes []int   `json:"keyframes"`
}

// Rate returns the index's media time base.
func (idx *Index) Rate() model.Rate { return model.Rate{Num: idx.MediaNum, Den: idx.MediaDen} }

// TrackKeys identifies the tracks in the input.
type TrackKeys struct {
	VideoTrackID uint32
	AudioTrackID uint32
}

// Build indexes the repaired timeline. Every sample gets an entry so byte-level
// attribution is complete; keyframes mark where decoding can resume.
func Build(tl *repair.Timeline, tracks TrackKeys) (*Index, error) {
	if err := tl.Validate(); err != nil {
		return nil, err
	}
	idx := &Index{MediaNum: tl.MediaRate.Num, MediaDen: tl.MediaRate.Den}
	for _, ft := range tl.Fragments {
		frag := ft.Fragment
		for _, tr := range ft.Tracks {
			trackStart, err := model.FromTicks(tr.Start.Ticks, tl.MediaRate, tr.Rate)
			if err != nil {
				return nil, model.E(model.CodeInvalidInput, "index",
					"fragment %s track %d repaired start does not land on %s", frag.Path, tr.TrackID, tr.Rate).
					WithPath(frag.Path).WithByte(tr.TFDTOffset)
			}
			origStart, err := model.FromTicks(int64(tr.OriginalTFD), tr.Rate, tl.MediaRate)
			if err != nil {
				return nil, model.E(model.CodeInvalidInput, "index",
					"fragment %s track %d original tfdt does not land on %s", frag.Path, tr.TrackID, tl.MediaRate).
					WithPath(frag.Path).WithByte(tr.TFDTOffset)
			}
			repairedMedia := tr.Start.Ticks
			var cursor uint32
			for _, s := range tr.Samples {
				// Audio frames are independently decodable, so every audio
				// sample is a valid random-access point. Video relies on the
				// sync flag from the fragment.
				key := s.IsSync() || tr.TrackID == tracks.AudioTrackID
				e := Entry{
					Source:             ft.Source,
					Fragment:           frag.Path,
					Sequence:           ft.Sequence,
					HasSequence:        ft.HasSequence,
					TrackID:            tr.TrackID,
					IsKeyframe:         key,
					TrackTicks:         trackStart + int64(cursor),
					TrackRate:          tr.Rate.String(),
					MediaTicks:         repairedMedia,
					OriginalMediaTicks: origStart,
					ByteOffset:         s.DataOffset,
					Size:               s.Size,
					FragmentByteOffset: frag.MoofOffset,
					Duration:           s.Duration,
				}
				durMedia, err := model.FromTicks(int64(s.Duration), tr.Rate, tl.MediaRate)
				if err != nil {
					return nil, model.E(model.CodeInvalidInput, "index",
						"fragment %s track %d sample duration %d does not land on %s",
						frag.Path, tr.TrackID, s.Duration, tl.MediaRate).
						WithPath(frag.Path).WithByte(s.DataOffset)
				}
				if e.IsKeyframe {
					idx.Keyframes = append(idx.Keyframes, len(idx.Entries))
				}
				idx.Entries = append(idx.Entries, e)
				repairedMedia += durMedia
				origStart += durMedia
				cursor += s.Duration
			}
		}
	}
	if len(idx.Entries) == 0 {
		return nil, model.E(CodeIndexInvalid, "build index", "timeline produced no entries")
	}
	idx.SortEntries()
	return idx, nil
}

// SeekResult is a resolved random-access request.
type SeekResult struct {
	TrackID      uint32  `json:"track_id"`
	TargetSec    float64 `json:"target_sec"`
	Key          Entry   `json:"key"`
	SampleOffset int64   `json:"sample_offset"`
	DistanceMs   float64 `json:"distance_ms"`
}

// Seek finds the latest keyframe at or before target on one track. A target
// before the track's first keyframe fails rather than snapping forward, so the
// caller sees an explicit decision instead of a silent jump.
func (idx *Index) Seek(trackID uint32, target model.Time) (*SeekResult, error) {
	if !target.Rate.Valid() {
		return nil, model.E(model.CodeInvalidInput, "seek", "invalid target rate %s", target.Rate)
	}
	rate := idx.Rate()
	var best *Entry
	for i := range idx.Entries {
		e := &idx.Entries[i]
		if e.TrackID != trackID || !e.IsKeyframe {
			continue
		}
		at := model.Time{Ticks: e.MediaTicks, Rate: rate}
		c, err := at.Compare(target)
		if err != nil {
			return nil, model.E(model.CodeInvalidInput, "seek", "cannot compare times: %v", err)
		}
		if c > 0 {
			continue
		}
		best = e
	}
	if best == nil {
		return nil, model.E(CodeNoKeyframe, "seek",
			"no keyframe on track %d at or before %.3fs", trackID, target.Seconds())
	}
	dist := target.Seconds() - float64(best.MediaTicks)*float64(rate.Den)/float64(rate.Num)
	return &SeekResult{
		TrackID:      trackID,
		TargetSec:    target.Seconds(),
		Key:          *best,
		SampleOffset: best.ByteOffset,
		DistanceMs:   dist * 1000,
	}, nil
}

// EntriesAt returns entries whose media time falls in [start,end), optionally
// restricted to a single track (trackID 0 means all tracks).
func (idx *Index) EntriesAt(trackID uint32, start, end model.Time) ([]Entry, error) {
	rate := idx.Rate()
	var out []Entry
	for _, e := range idx.Entries {
		if trackID != 0 && e.TrackID != trackID {
			continue
		}
		at := model.Time{Ticks: e.MediaTicks, Rate: rate}
		if c, err := at.Compare(start); err == nil && c >= 0 {
			if c2, err := at.Compare(end); err == nil && c2 < 0 {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// Validate checks structural invariants: periods per track are ascending,
// offsets are non-negative, and every track has at least one keyframe.
func (idx *Index) Validate() error {
	tracks := map[uint32]bool{}
	lastByTrack := map[uint32]int64{}
	keyframeByTrack := map[uint32]bool{}
	for _, e := range idx.Entries {
		tracks[e.TrackID] = true
		if e.IsKeyframe {
			keyframeByTrack[e.TrackID] = true
		}
		if e.ByteOffset < 0 {
			return model.E(CodeIndexInvalid, "validate index", "negative byte offset in %s", e.Fragment).WithByte(e.ByteOffset)
		}
		if e.Size == 0 {
			return model.E(CodeIndexInvalid, "validate index", "zero-size sample in %s", e.Fragment).WithByte(e.ByteOffset)
		}
		if prev, ok := lastByTrack[e.TrackID]; ok && e.MediaTicks < prev {
			return model.E(model.CodeNonMonotonic, "validate index",
				"track %d entry time %d before %d", e.TrackID, e.MediaTicks, prev).WithRange(e.MediaTicks, prev)
		}
		lastByTrack[e.TrackID] = e.MediaTicks
	}
	for t := range tracks {
		if !keyframeByTrack[t] {
			return model.E(CodeIndexInvalid, "validate index", "track %d has no keyframe", t)
		}
	}
	return nil
}

// Range returns the media range covered by the index.
func (idx *Index) Range() (model.Time, model.Time) {
	r := idx.Rate()
	if len(idx.Entries) == 0 {
		return model.MustTime(0, r), model.MustTime(0, r)
	}
	start, end := idx.Entries[0].MediaTicks, idx.Entries[0].MediaTicks
	for _, e := range idx.Entries {
		if e.MediaTicks < start {
			start = e.MediaTicks
		}
		fin := e.MediaTicks + int64(e.Duration)
		if fin > end {
			end = fin
		}
	}
	return model.MustTime(start, r), model.MustTime(end, r)
}

// SortEntries orders entries by media time, then track, then offset, so
// serialization is deterministic regardless of fragment arrival order.
func (idx *Index) SortEntries() {
	sort.SliceStable(idx.Entries, func(i, j int) bool {
		a, b := idx.Entries[i], idx.Entries[j]
		if a.MediaTicks != b.MediaTicks {
			return a.MediaTicks < b.MediaTicks
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		return a.ByteOffset < b.ByteOffset
	})
	idx.rebuildKeyframes()
}

func (idx *Index) rebuildKeyframes() {
	idx.Keyframes = idx.Keyframes[:0]
	for i, e := range idx.Entries {
		if e.IsKeyframe {
			idx.Keyframes = append(idx.Keyframes, i)
		}
	}
}

// MarshalJSON keeps field order stable for hashing.
func (idx *Index) MarshalJSON() ([]byte, error) {
	type alias Index
	return json.Marshal((*alias)(idx))
}

// Summary renders counts for terminal output.
func (idx *Index) Summary() string {
	return fmt.Sprintf("entries=%d keyframes=%d", len(idx.Entries), len(idx.Keyframes))
}
