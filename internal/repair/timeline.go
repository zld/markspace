// Package repair makes media timelines monotonic per source and track.
// Repairs never guess: every change is recorded with the source, fragment,
// track, original value, repaired value and the byte offset of the original
// tfdt field.
package repair

import (
	"fmt"
	"sort"

	"rtx/internal/cmaf"
	"rtx/internal/model"
)

// Code values for this package.
const (
	// CodeTFDTBackwards marks a tfdt that moved a track before its previous end.
	CodeTFDTBackwards model.Code = "tfdt_backward"
	// CodeTimelineGap marks track data missing between fragments.
	CodeTimelineGap model.Code = "timeline_gap"
)

// Repair records one accepted tfdt adjustment. Original, Repaired and
// PreviousEnd are in the track's own timescale; MediaStart is on the shared
// media grid including the source offset.
type Repair struct {
	Source      string `json:"source"`
	Fragment    string `json:"fragment"`
	TrackID     uint32 `json:"track_id"`
	Original    uint64 `json:"original_tfdt"`
	Repaired    uint64 `json:"repaired_tfdt"`
	PreviousEnd uint64 `json:"previous_end"`
	ByteOffset  int64  `json:"byte_offset"`
	MediaOffset int64  `json:"source_media_offset"`
	MediaStart  int64  `json:"media_start"`
	Reason      string `json:"reason"`
}

// TrackSpan is one track's continuous media span in one fragment.
type TrackSpan struct {
	TrackID uint32
	Rate    model.Rate
	// Start and End are on the shared media grid, including SourceOffset.
	Start model.Time
	End   model.Time
	// SourceOffset is the explicit per-source media offset in media ticks.
	SourceOffset int64
	// OriginalTFD and RepairedTFD are raw tfdt values in the track timescale.
	OriginalTFD uint64
	RepairedTFD uint64
	TFDTOffset  int64
	Samples     []cmaf.Sample
}

// FragmentTimeline is a fragment's track spans after repair.
type FragmentTimeline struct {
	Source   string
	Fragment *cmaf.Fragment
	Sequence uint32
	// HasSequence is false when mfhd was absent.
	HasSequence bool
	// Order is the fragment's declared order within its source.
	Order  int
	Tracks []TrackSpan
}

// SequenceGap is a skipped mfhd sequence number. The gap is recorded, never
// converted into a media duration.
type SequenceGap struct {
	Source  string   `json:"source"`
	After   uint32   `json:"after"`
	Before  uint32   `json:"before"`
	Missing []uint32 `json:"missing"`
}

// Timeline is the repaired media timeline for a whole run.
type Timeline struct {
	MediaRate model.Rate
	VideoRate model.Rate
	AudioRate model.Rate
	// Offsets maps source id to explicit media offset in media ticks.
	Offsets      map[string]int64
	Fragments    []*FragmentTimeline
	Repairs      []Repair
	SequenceGaps []SequenceGap
}

// TrackSpecs describes the per-track timescales used to interpret tfdt.
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

// Input couples a parsed fragment with its source and declared order.
type Input struct {
	Source string
	Order  int
	Frag   *cmaf.Fragment
}

// BuildTimeline processes inputs in declared order, adding each source's
// explicit offset and raising any tfdt that rewinds its own source/track.
func BuildTimeline(mediaRate model.Rate, specs TrackSpecs, offsets map[string]int64, inputs []Input) (*Timeline, error) {
	videoRate, err := model.NewRate(specs.VideoTimescale)
	if err != nil {
		return nil, model.E(model.CodeInvalidInput, "timeline", "%v", err)
	}
	audioRate, err := model.NewRate(specs.AudioTimescale)
	if err != nil {
		return nil, model.E(model.CodeInvalidInput, "timeline", "%v", err)
	}
	tl := &Timeline{MediaRate: mediaRate, VideoRate: videoRate, AudioRate: audioRate, Offsets: map[string]int64{}}
	for k, v := range offsets {
		tl.Offsets[k] = v
	}

	type key struct {
		source string
		track  uint32
	}
	lastEnd := map[key]int64{}
	lastSeq := map[string]*uint32{}

	for _, in := range inputs {
		frag := in.Frag
		if frag == nil || frag.IsInit {
			continue
		}
		offset, ok := offsets[in.Source]
		if !ok {
			return nil, model.E(model.CodeAnchorAmbiguous, "timeline",
				"source %s has no explicit media offset", in.Source).WithPath(frag.Path)
		}
		ft := &FragmentTimeline{Source: in.Source, Fragment: frag, Order: in.Order}
		if frag.MFHDSequence != nil {
			ft.Sequence = *frag.MFHDSequence
			ft.HasSequence = true
			if prev := lastSeq[in.Source]; prev != nil && ft.Sequence > *prev+1 {
				g := SequenceGap{Source: in.Source, After: *prev, Before: ft.Sequence}
				for s := *prev + 1; s < ft.Sequence; s++ {
					g.Missing = append(g.Missing, s)
				}
				tl.SequenceGaps = append(tl.SequenceGaps, g)
			}
			seq := ft.Sequence
			lastSeq[in.Source] = &seq
		}
		// A missing mfhd neither invents nor closes a gap: retain the last
		// known sequence so a later fragment still reports the gap.

		for i := range frag.Tracks {
			tr := frag.Tracks[i]
			var rate model.Rate
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
			localStart, err := model.FromTicks(int64(tr.BaseMediaDecodeTime), rate, mediaRate)
			if err != nil {
				return nil, model.E(model.CodeInvalidInput, "timeline",
					"fragment %s track %d tfdt %d does not map exactly onto %s",
					frag.Path, tr.TrackID, tr.BaseMediaDecodeTime, mediaRate).
					WithPath(frag.Path).WithByte(tr.TFDTOffset).
					WithRange(int64(tr.BaseMediaDecodeTime), int64(tr.BaseMediaDecodeTime))
			}
			durMedia, err := model.FromTicks(durTicks, rate, mediaRate)
			if err != nil {
				return nil, model.E(model.CodeInvalidInput, "timeline",
					"fragment %s track %d duration %d does not map exactly onto %s",
					frag.Path, tr.TrackID, durTicks, mediaRate).WithPath(frag.Path).WithByte(tr.TFDTOffset)
			}
			k := key{source: in.Source, track: tr.TrackID}
			repairedLocal := localStart
			repairedTFD := tr.BaseMediaDecodeTime
			if prevEnd, seen := lastEnd[k]; seen && localStart < prevEnd {
				prevEndTrack, err := model.FromTicks(prevEnd, mediaRate, rate)
				if err != nil {
					return nil, model.E(CodeTFDTBackwards, "timeline",
						"fragment %s track %d: tfdt %d is behind %d and cannot be raised onto the %s grid",
						frag.Path, tr.TrackID, tr.BaseMediaDecodeTime, prevEnd, rate).
						WithPath(frag.Path).WithByte(tr.TFDTOffset).
						WithRange(localStart, localStart+durMedia)
				}
				repairedTFD = uint64(prevEndTrack)
				repairedLocal = prevEnd
				tl.Repairs = append(tl.Repairs, Repair{
					Source:      in.Source,
					Fragment:    frag.Path,
					TrackID:     tr.TrackID,
					Original:    tr.BaseMediaDecodeTime,
					Repaired:    repairedTFD,
					PreviousEnd: uint64(prevEndTrack),
					ByteOffset:  tr.TFDTOffset,
					MediaOffset: offset,
					MediaStart:  offset + prevEnd,
					Reason:      "tfdt_rollback",
				})
			}
			start := offset + repairedLocal
			span := TrackSpan{
				TrackID:      tr.TrackID,
				Rate:         rate,
				Start:        model.MustTime(start, mediaRate),
				End:          model.MustTime(start+durMedia, mediaRate),
				SourceOffset: offset,
				OriginalTFD:  tr.BaseMediaDecodeTime,
				RepairedTFD:  repairedTFD,
				TFDTOffset:   tr.TFDTOffset,
				Samples:      tr.Samples,
			}
			lastEnd[k] = repairedLocal + durMedia
			ft.Tracks = append(ft.Tracks, span)
		}
		tl.Fragments = append(tl.Fragments, ft)
	}
	sort.SliceStable(tl.Fragments, func(i, j int) bool {
		si, sj := tl.Fragments[i].startTicks(), tl.Fragments[j].startTicks()
		if si != sj {
			return si < sj
		}
		if tl.Fragments[i].Source != tl.Fragments[j].Source {
			return tl.Fragments[i].Source < tl.Fragments[j].Source
		}
		return tl.Fragments[i].Order < tl.Fragments[j].Order
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

// Validate checks that every track is monotonic on the global media grid and
// that no span is inverted. It runs after repairs and before indexing.
func (tl *Timeline) Validate() error {
	last := map[uint32]int64{}
	for _, ft := range tl.Fragments {
		for _, tr := range ft.Tracks {
			c, err := tr.Start.Compare(tr.End)
			if err != nil || c > 0 {
				return model.E(model.CodeNonMonotonic, "validate timeline",
					"track %d span is inverted", tr.TrackID).
					WithRange(tr.Start.Ticks, tr.End.Ticks).WithPath(ft.Fragment.Path)
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

// RepairedFragments counts distinct fragments with at least one repair.
func (tl *Timeline) RepairedFragments() int {
	seen := map[string]bool{}
	for _, r := range tl.Repairs {
		seen[r.Source+"\x00"+r.Fragment] = true
	}
	return len(seen)
}

// Summary renders a one-line description used by terminal output.
func (tl *Timeline) Summary() string {
	return fmt.Sprintf("fragments=%d repairs=%d seq_gaps=%d", len(tl.Fragments), len(tl.Repairs), len(tl.SequenceGaps))
}
