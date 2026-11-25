// Package splitter divides validated fragment groups into manifest segments
// along the common microsecond time axis. It never assumes a fixed frame
// rate: every boundary is snapped to a real sample using that sample's own
// duration ticks, and the adjustment between requested and actual boundary
// is recorded.
package splitter

import (
	"sort"

	"mediad/internal/mp4"
	"mediad/internal/timeline"
)

// MappedSample is one sample placed on the common axis, retaining its input
// byte location for manifest offsets.
type MappedSample struct {
	TrackID   uint32
	Fragment  string
	SeqNum    uint32
	Index     int
	StartUS   int64
	EndUS     int64
	AdjustUS  int64
	Offset    int64 // absolute input byte offset of sample data
	Size      int64
	Invalid   bool
	ErrMsg    string
}

// Segment is one manifest entry.
type Segment struct {
	ID            int    `json:"id"`
	TrackID       uint32 `json:"track_id"`
	SeqNum        uint32 `json:"seq_num"`
	InputOffset   int64  `json:"input_offset"`
	StartUS       int64  `json:"start_us"`
	EndUS         int64  `json:"end_us"`
	TargetEndUS   int64  `json:"target_end_us"`
	AdjustUS      int64  `json:"adjust_us"` // actual boundary minus requested
	SampleCount   int    `json:"sample_count"`
	SpansInvalid  bool   `json:"spans_invalid"`
}

// Config parameterises splitting.
type Config struct {
	TargetSegmentUS int64 // requested segment duration on the common axis
	Timescales      map[uint32]uint32
	DriftPPMMax     int64
}

// MapTrack converts one track fragment's samples into common-axis samples,
// reusing a per-track clock so tfdt rollover stays continuous across
// fragments. Unexplained rollbacks are surfaced as invalid intervals rather
// than fabricated times.
func MapTrack(clock *timeline.TrackClock, track *mp4.TrackFragment, frag *mp4.Fragment) []MappedSample {
	out := make([]MappedSample, 0, len(track.Samples))
	tickBase := track.BaseMediaT
	for _, s := range track.Samples {
		st := clock.MapTick(s.Index, tickBase, uint64(max64(s.Duration, 0)))
		ms := MappedSample{
			TrackID:  track.TrackID,
			Fragment: frag.File,
			SeqNum:   frag.SeqNum,
			Index:    s.Index,
			StartUS:  st.StartUS,
			EndUS:    st.EndUS,
			AdjustUS: st.AdjustUS,
			Offset:   s.DataOffset,
			Size:     s.Size,
			Invalid:  st.Invalid,
			ErrMsg:   st.ErrMsg,
		}
		out = append(out, ms)
		tickBase += uint64(max64(s.Duration, 0))
	}
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Split groups mapped samples of one track into segments. Boundaries are
// snapped to the nearest real sample; the adjustment versus the requested
// boundary is recorded per segment. No fixed frame rate is ever assumed.
func Split(trackID uint32, samples []MappedSample, cfg Config) []Segment {
	if len(samples) == 0 {
		return nil
	}
	// Partition unmappable (no-time) samples from timed ones. No-time samples
	// are reported as their own flagged segment instead of distorting the
	// common-axis boundaries; invalid samples that still carry explicit time
	// stay inline and merely flag whichever segment contains them.
	var timed, noTime []MappedSample
	for _, s := range samples {
		if s.Invalid && s.StartUS == 0 && s.EndUS == 0 {
			noTime = append(noTime, s)
		} else {
			timed = append(timed, s)
		}
	}

	segs := splitTimed(trackID, timed, cfg)

	if len(noTime) > 0 {
		// Anchor the invalid segment at the last valid boundary when one
		// exists so its placement is deterministic; otherwise at zero.
		var startUS int64
		var seq uint32
		var offset int64
		if n := len(segs); n > 0 {
			startUS = segs[n-1].EndUS
			seq = noTime[0].SeqNum
			offset = noTime[0].Offset
		}
		segs = append(segs, Segment{
			ID: len(segs) + 1, TrackID: trackID, SeqNum: seq,
			InputOffset: offset, StartUS: startUS, EndUS: startUS,
			SampleCount: len(noTime), SpansInvalid: true,
		})
	}
	return segs
}

// splitTimed splits only samples that carry a real common-axis time.
func splitTimed(trackID uint32, sorted []MappedSample, cfg Config) []Segment {
	if len(sorted) == 0 {
		return nil
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].StartUS < sorted[j].StartUS })

	// Collect every real sample edge (start and end) on the axis once, then
	// for each requested boundary snap to the closest edge.
	edges := make([]int64, 0, len(sorted)*2)
	for _, s := range sorted {
		edges = append(edges, s.StartUS, s.EndUS)
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i] < edges[j] })
	nearestEdge := func(b int64) int64 {
		// binary search for insertion point
		idx := sort.Search(len(edges), func(k int) bool { return edges[k] >= b })
		if idx == 0 {
			return edges[0]
		}
		if idx >= len(edges) {
			return edges[len(edges)-1]
		}
		if edges[idx] == b || edges[idx]-b < b-edges[idx-1] {
			return edges[idx]
		}
		return edges[idx-1]
	}

	var segs []Segment
	origin := sorted[0].StartUS
	segmentStart := origin
	target := origin + cfg.TargetSegmentUS
	cur := Segment{
		ID: 1, TrackID: trackID, SeqNum: sorted[0].SeqNum,
		InputOffset: sorted[0].Offset, StartUS: origin, TargetEndUS: target,
	}

	for i, s := range sorted {
		if i > 0 && s.StartUS >= target {
			// Close the current segment on the nearest real edge to target.
			boundary := nearestEdge(target)
			if boundary <= segmentStart {
				boundary = s.StartUS
			}
			cur.EndUS = boundary
			cur.AdjustUS = boundary - cur.TargetEndUS
			segs = append(segs, cur)
			cur = Segment{
				ID: len(segs) + 1, TrackID: trackID, SeqNum: s.SeqNum,
				InputOffset: s.Offset, StartUS: boundary,
			}
			segmentStart = boundary
			target = segmentStart + cfg.TargetSegmentUS
			cur.TargetEndUS = target
		}
		cur.SampleCount++
		if s.Invalid {
			cur.SpansInvalid = true
		}
		cur.EndUS = s.EndUS
	}
	cur.AdjustUS = cur.EndUS - cur.TargetEndUS
	segs = append(segs, cur)
	return segs
}

// Run maps every track of every fragment and splits each independently. A
// single clock is maintained per track for the whole group, so rollover and
// rollback decisions span fragment boundaries.
func Run(frags []*mp4.Fragment, cfg Config) (map[uint32][]MappedSample, map[uint32][]Segment, error) {
	mapped, segments, _, err := RunWithClocks(frags, cfg)
	return mapped, segments, err
}

// RunWithClocks behaves like Run but also returns the per-track clocks so a
// caller can read rollover counts and run explicit clock-drift checks.
func RunWithClocks(frags []*mp4.Fragment, cfg Config) (
	map[uint32][]MappedSample, map[uint32][]Segment, map[uint32]*timeline.TrackClock, error,
) {
	clocks := map[uint32]*timeline.TrackClock{}
	mapped := map[uint32][]MappedSample{}
	for _, f := range frags {
		for _, tf := range f.Tracks {
			clock, ok := clocks[tf.TrackID]
			if !ok {
				clock = timeline.NewTrackClock(timeline.ClockConfig{
					TrackID:     tf.TrackID,
					Timescale:   cfg.Timescales[tf.TrackID],
					DriftPPMMax: cfg.DriftPPMMax,
				})
				clocks[tf.TrackID] = clock
			}
			mapped[tf.TrackID] = append(mapped[tf.TrackID], MapTrack(clock, tf, f)...)
		}
	}
	segments := map[uint32][]Segment{}
	for trackID, ms := range mapped {
		segments[trackID] = Split(trackID, ms, cfg)
	}
	return mapped, segments, clocks, nil
}
