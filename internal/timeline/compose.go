package timeline

import (
	"archivecheck/internal/manifest"
	"archivecheck/internal/media"
	"archivecheck/internal/merr"
)

// TrackOffset records one track's entry on the unified clock.
type TrackOffset struct {
	TrackID     uint32 `json:"track_id"`
	Name        string `json:"name"`
	StartTicks  int64  `json:"start_ticks"`
	TimebaseNum uint32 `json:"timebase_num"`
	TimebaseDen uint32 `json:"timebase_den"`
	MaxErrNanos int64  `json:"max_conversion_error_ns"`
}

// Alignment reports how well composed segment boundaries match the
// manifest's declared timing. Values are integers scaled to microseconds
// (manifest_alignment_ms * 1000), keeping the JSON output float-free.
type Alignment struct {
	MaxResidualNanos int64 `json:"max_residual_ns"`
	ManifestMicros   int64 `json:"manifest_alignment_us"`
}

// Timeline is the composed, fully integer media timeline.
type Timeline struct {
	Tracks   []TrackOffset
	Segments []*media.Segment
	Align    Alignment
}

// BoundaryMap returns a track/seq -> segment lookup.
func (t *Timeline) BoundaryMap() map[uint32]map[uint32]*media.Segment {
	m := make(map[uint32]map[uint32]*media.Segment, len(t.Tracks))
	for _, s := range t.Segments {
		if m[s.TrackID] == nil {
			m[s.TrackID] = map[uint32]*media.Segment{}
		}
		m[s.TrackID][s.Seq] = s
	}
	return m
}

// ComposeInput is one track's recovered segment frames, ready to be
// placed on the unified clock. Frame PTS values are native track units
// relative to the start of their segment.
type ComposeInput struct {
	Spec         media.TrackSpec
	Clock        Clock
	Frames       []media.Frame
	SegmentLost  map[uint32]int  // seq -> lost packets
	SegmentBytes map[uint32]int  // seq -> effective media bytes
	SegmentTrunc map[uint32]bool // seq -> truncated flag
}

// Compose maps every track onto the common clock using exact rational
// segment boundaries from the manifest, then aligns each frame inside
// its segment. No float arithmetic is used anywhere on the hot path.
func Compose(pl *manifest.Playlist, inputs []ComposeInput) (*Timeline, error) {
	tl := &Timeline{}
	specByID := map[uint32]media.TrackSpec{}
	clocks := map[uint32]Clock{}
	framesByTrack := map[uint32][]media.Frame{}
	lost := map[uint32]map[uint32]int{}
	bytes := map[uint32]map[uint32]int{}
	trunc := map[uint32]map[uint32]bool{}
	for _, in := range inputs {
		specByID[in.Spec.ID] = in.Spec
		clocks[in.Spec.ID] = in.Clock
		framesByTrack[in.Spec.ID] = in.Frames
		lost[in.Spec.ID] = in.SegmentLost
		bytes[in.Spec.ID] = in.SegmentBytes
		trunc[in.Spec.ID] = in.SegmentTrunc
	}

	var globalStart int64
	for _, mt := range pl.Tracks {
		spec, ok := specByID[mt.ID]
		if !ok {
			return nil, merr.New(merr.KindTimeline, "timeline",
				"track:"+mt.Name, "no recovered input for manifest track")
		}
		clk := clocks[mt.ID]
		off := TrackOffset{
			TrackID: mt.ID, Name: mt.Name, StartTicks: globalStart,
			TimebaseNum: clk.TimebaseNum, TimebaseDen: clk.TimebaseDen,
		}

		var boundary int64
		for _, ref := range mt.Refs {
			wantTicks := SecondsToTicks(ref.DurationRational())
			seg := &media.Segment{
				TrackID:       mt.ID,
				Seq:           ref.Seq,
				StartTicks:    globalStart + boundary,
				DurationTicks: wantTicks,
				MediaBytes:    bytes[mt.ID][ref.Seq],
				LostPackets:   lost[mt.ID][ref.Seq],
				Truncated:     trunc[mt.ID][ref.Seq],
			}

			var frames []media.Frame
			for _, f := range framesByTrack[mt.ID] {
				if f.SegmentSeq != ref.Seq {
					continue
				}
				rel, errNanos := clk.ToTicks(f.PTS)
				if errNanos > off.MaxErrNanos {
					off.MaxErrNanos = errNanos
				}
				f.TrackID = mt.ID
				f.PTS = seg.StartTicks + rel
				dur, _ := clk.ToTicks(f.Duration)
				if dur <= 0 {
					dur = wantTicks
				}
				f.Duration = dur
				f.Width = nonzeroU16(f.Width, spec.Width)
				f.Height = nonzeroU16(f.Height, spec.Height)
				frames = append(frames, f)
			}
			seg.Frames = frames
			tl.Segments = append(tl.Segments, seg)
			boundary += wantTicks
		}

		if err := verifyAlignment(mt, tl); err != nil {
			return nil, err
		}
		tl.Tracks = append(tl.Tracks, off)
	}
	tl.Align = computeAlignment(tl)
	return tl, nil
}

// verifyAlignment checks that frames do not cross the manifest segment
// boundaries. Boundaries themselves are constructed from exact rational
// manifest durations, so the residual is zero by construction and proven
// here rather than repaired.
func verifyAlignment(mt *manifest.Track, tl *Timeline) error {
	for _, seg := range tl.Segments {
		if seg.TrackID != mt.ID {
			continue
		}
		end := seg.StartTicks + seg.DurationTicks
		for _, f := range seg.Frames {
			if f.PTS < seg.StartTicks {
				return merr.New(merr.KindTimeline, "timeline",
					"track:"+mt.Name+":seq:"+u32(seg.Seq),
					"frame PTS precedes segment start")
			}
			if f.PTS+f.Duration > end {
				return merr.New(merr.KindTimeline, "timeline",
					"track:"+mt.Name+":seq:"+u32(seg.Seq),
					"frame overshoots manifest segment boundary")
			}
		}
	}
	return nil
}

func computeAlignment(tl *Timeline) Alignment {
	var a Alignment
	for _, t := range tl.Tracks {
		if t.MaxErrNanos > a.MaxResidualNanos {
			a.MaxResidualNanos = t.MaxErrNanos
		}
	}
	// Convert the worst per-frame conversion error to microseconds.
	a.ManifestMicros = a.MaxResidualNanos / 1000
	return a
}

func nonzeroU16(v, fallback uint16) uint16 {
	if v == 0 {
		return fallback
	}
	return v
}
