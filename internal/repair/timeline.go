// Package repair makes media timelines monotonic per track. Repairs never
// guess: every change is recorded with the source, fragment, track, original
// value, repaired value and the byte offset of the original tfdt field.
package repair

import (
	"fmt"
	"sort"

	"rtx/internal/cmaf"
	"rtx/internal/model"
)

// Code values for this package.
const (
	// CodeTFDTBackwards marks a tfdt that moved the track before its previous
	// end (a rollback).
	CodeTFDTBackwards model.Code = "tfdt_backward"
	// CodeTimelineGap marks track data that is missing between fragments.
	CodeTimelineGap model.Code = "timeline_gap"
)

// Repair records one accepted tfdt adjustment.
type Repair struct {
	Source      string `json:"source"`
	Fragment    string `json:"fragment"`
	TrackID     uint32 `json:"track_id"`
	Original    uint64 `json:"original_tfdt"`
	Repaired    uint64 `json:"repaired_tfdt"`
	PreviousEnd uint64 `json:"previous_end"`
	ByteOffset  int64  `json:"byte_offset"`
	Reason      string `json:"reason"`
}

// FragmentTimeline is a fragment's track spans after repair, in the shared
// media grid. Offsets are absolute in the fragment file.
type FragmentTimeline struct {
	Source   string
	Fragment *cmaf.Fragment
	Sequence uint32
	// HasSequence is false when mfhd was absent.
	HasSequence bool
	Tracks      []TrackSpan
}

// TrackSpan is one track's continuous media span in one fragment.
type TrackSpan struct {
	TrackID     uint32
	Rate        model.Rate
	Start       model.Time
	End         model.Time
	OriginalTFD uint64
	RepairedTFD uint64
	TFDTOffset  int64
	Samples     []cmaf.Sample
}

// Timeline is the repaired media timeline for a whole run.
type Timeline struct {
	MediaRate model.Rate
	// VideoRate and AudioRate are each track's own timescale.
	VideoRate model.Rate
	AudioRate model.Rate
	Fragments []*FragmentTimeline
	Repairs   []Repair
	// SequenceGaps lists mfhd sequence numbers that were skipped. The gap is
	// recorded, never converted to a media duration.
	SequenceGaps []SequenceGap
	// SourceSwitches lists detected source changes with their anchors.
	SourceSwitches []cmaf.Event
}

// SequenceGap is a skipped mfhd sequence number.
type SequenceGap struct {
	Source  string   `json:"source"`
	After   uint32   `json:"after"`
	Before  uint32   `json:"before"`
	Missing []uint32 `json:"missing"`
}

// TrackSpecs describes the per-track timescale used to interpret tfdt.
type TrackSpecs struct {
	VideoTrackID   uint32
	VideoTimescale uint32
	AudioTrackID   uint32
	AudioTimescale uint32
}

// DefaultTrackSpecs match the fixture layout.
func DefaultTrackSpecs() TrackSpecs {
	return TrackSpecs{VideoTrackID: 1, VideoTimescale: 90000, AudioTrackID: 2, AudioTimescale: 48000}
}

// BuildTimeline reparses the fragments (already parsed by the caller) in the
// given order and resets any track whose tfdt would rewind. A fragment whose
// tfdt is behind the previous end of the same track is moved forward to that
// end, and the original value and byte offset are recorded. A fragment whose
// declared size cannot be read is rejected upstream; this function never
// invents samples.
func BuildTimeline(mediaRate model.Rate, specs TrackSpecs, inputs []*cmaf.Fragment) (*Timeline, error) {
	videoRate, err := model.NewRate(specs.VideoTimescale)
	if err != nil {
		return nil, model.E(model.CodeInvalidInput, "timeline", "%v", err)
	}
	audioRate, err := model.NewRate(specs.AudioTimescale)
	if err != nil {
		return nil, model.E(model.CodeInvalidInput, "timeline", "%v", err)
	}
	tl := &Timeline{MediaRate: mediaRate, VideoRate: videoRate, AudioRate: audioRate}

	// Last decode end per (source, track), in media ticks.
	type key struct {
		source string
		track  uint32
	}
	lastEnd := map[key]int64{}
	lastSeq := map[string]*uint32{}

	for _, frag := range inputs {
		if frag.IsInit {
			continue
		}
		ft := &FragmentTimeline{Source: frag.Source, Fragment: frag}
		if frag.MFHDSequence != nil {
			ft.Sequence = *frag.MFHDSequence
			ft.HasSequence = true
		}
		// mfhd gap detection per source.
		if ft.HasSequence {
			if prev, ok := lastSeq[frag.Source]; ok && prev != nil && ft.Sequence > *prev+1 {
				g := SequenceGap{Source: frag.Source, After: *prev, Before: ft.Sequence}
				for s := *prev + 1; s < ft.Sequence; s++ {
					g.Missing = append(g.Missing, s)
				}
				tl.SequenceGaps = append(tl.SequenceGaps, g)
			}
			seq := ft.Sequence
			lastSeq[frag.Source] = &seq
		}
		// A missing mfhd neither invents nor closes a sequence gap: the last
		// known sequence is retained so a later fragment still reports it.

		for i := range frag.Tracks {
			tr := frag.Tracks[i]
			rate := videoRate
			switch tr.TrackID {
			case specs.AudioTrackID:
				rate = audioRate
			case specs.VideoTrackID:
				rate = videoRate
			default:
				return nil, model.E(cmaf.CodeCMAFInvalid, "timeline",
					"fragment %s has unknown track %d", frag.Path, tr.TrackID).WithPath(frag.Path)
			}
			var durTicks int64
			for _, s := range tr.Samples {
				durTicks += int64(s.Duration)
			}
			startTicks, err := model.FromTicks(int64(tr.BaseMediaDecodeTime), rate, mediaRate)
			if err != nil {
				return nil, model.E(model.CodeInvalidInput, "timeline",
					"fragment %s track %d tfdt %d does not map exactly onto %s",
					frag.Path, tr.TrackID, tr.BaseMediaDecodeTime, mediaRate).WithPath(frag.Path).WithRange(int64(tr.BaseMediaDecodeTime), int64(tr.BaseMediaDecodeTime)).WithByte(tr.TFDTOffset)
			}
			durMedia, err := model.FromTicks(durTicks, rate, mediaRate)
			if err != nil {
				return nil, model.E(model.CodeInvalidInput, "timeline",
					"fragment %s track %d duration %d does not map exactly onto %s",
					frag.Path, tr.TrackID, durTicks, mediaRate).WithPath(frag.Path).WithByte(tr.TFDTOffset)
			}
			k := key{source: frag.Source, track: tr.TrackID}
			repaired := startTicks
			prevEnd, seen := lastEnd[k]
			repairedTFD := tr.BaseMediaDecodeTime
			if seen && startTicks < prevEnd {
				// A rewrite to prevEnd must land on the track's own grid.
				newTicks, err := model.FromTicks(prevEnd, mediaRate, rate)
				if err != nil {
					return nil, model.E(CodeTFDTBackwards, "timeline",
						"fragment %s track %d: tfdt %d is behind %d and cannot be raised onto the %s grid",
						frag.Path, tr.TrackID, tr.BaseMediaDecodeTime, prevEnd, rate).
						WithPath(frag.Path).WithByte(tr.TFDTOffset).
						WithRange(startTicks, startTicks+durMedia)
				}
				repairedTFD = uint64(newTicks)
				prevEndTrack, err := model.FromTicks(prevEnd, mediaRate, rate)
				if err != nil {
					return nil, model.E(CodeTFDTBackwards, "timeline",
						"fragment %s track %d: previous end %d does not map onto %s",
						frag.Path, tr.TrackID, prevEnd, rate).WithPath(frag.Path).WithByte(tr.TFDTOffset)
				}
				tl.Repairs = append(tl.Repairs, Repair{
					Source:      frag.Source,
					Fragment:    frag.Path,
					TrackID:     tr.TrackID,
					Original:    tr.BaseMediaDecodeTime,
					Repaired:    repairedTFD,
					PreviousEnd: uint64(prevEndTrack),
					ByteOffset:  tr.TFDTOffset,
					Reason:      "tfdt_rollback",
				})
				repaired = prevEnd
			}
			span := TrackSpan{
				TrackID:     tr.TrackID,
				Rate:        rate,
				Start:       model.MustTime(repaired, mediaRate),
				End:         model.MustTime(repaired+durMedia, mediaRate),
				OriginalTFD: tr.BaseMediaDecodeTime,
				RepairedTFD: repairedTFD,
				TFDTOffset:  tr.TFDTOffset,
				Samples:     tr.Samples,
			}
			lastEnd[k] = repaired + durMedia
			ft.Tracks = append(ft.Tracks, span)
		}
		tl.Fragments = append(tl.Fragments, ft)
	}
	// Order the timeline by media start so sequence order does not matter.
	sort.SliceStable(tl.Fragments, func(i, j int) bool {
		return tl.Fragments[i].startTicks() < tl.Fragments[j].startTicks()
	})
	return tl, nil
}

func (ft *FragmentTimeline) startTicks() int64 {
	if len(ft.Tracks) == 0 {
		return 0
	}
	min := ft.Tracks[0].Start.Ticks
	for _, t := range ft.Tracks[1:] {
		if t.Start.Ticks < min {
			min = t.Start.Ticks
		}
	}
	return min
}

// Validate checks that every track is monotonic across the timeline and that
// no track has an inverted span. It is a defense-in-depth check run after
// repairs and before indexing.
func (tl *Timeline) Validate() error {
	last := map[uint32]int64{}
	for _, ft := range tl.Fragments {
		for _, tr := range ft.Tracks {
			c, err := tr.Start.Compare(tr.End)
			if err != nil || c > 0 {
				return model.E(model.CodeNonMonotonic, "validate timeline",
					"track %d span is inverted", tr.TrackID).WithRange(tr.Start.Ticks, tr.End.Ticks)
			}
			if prev, ok := last[tr.TrackID]; ok && tr.Start.Ticks < prev {
				return model.E(model.CodeNonMonotonic, "validate timeline",
					"track %d starts at %d before previous end %d in %s", tr.TrackID, tr.Start.Ticks, prev, ft.Fragment.Path).
					WithRange(tr.Start.Ticks, tr.End.Ticks)
			}
			last[tr.TrackID] = tr.End.Ticks
		}
	}
	return nil
}

// RepairCount returns how many tfdt values were rewritten.
func (tl *Timeline) RepairCount() int { return len(tl.Repairs) }

// Summary renders a one-line description used by terminal output.
func (tl *Timeline) Summary() string {
	return fmt.Sprintf("fragments=%d repairs=%d seq_gaps=%d", len(tl.Fragments), len(tl.Repairs), len(tl.SequenceGaps))
}
