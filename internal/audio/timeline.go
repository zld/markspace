package audio

import (
	"sort"

	"rtpregress/internal/clock"
)

// Placement maps one segment onto the canonical 48 kHz sample timeline.
type Placement struct {
	SegmentID   int
	PID         uint16
	StartSample int64 // first sample index at TargetRate
	StartNanos  int64
	Samples     int
}

// Span is a timeline interval with no trustworthy source audio. The muxer
// bridges it with silence for container continuity; it is always reported in
// the manifest so no loss is silently hidden.
type Span struct {
	FromSample int64
	ToSample   int64
	FromNanos  int64
	ToNanos    int64
	Code       clock.Code
}

// Planner places VFR segments onto one monotonic sample grid using their real
// PTS. It never fabricates source audio: holes become explicit silence spans.
type Planner struct {
	tb     *clock.Timebase
	placed []Placement
}

// NewPlanner builds a planner over the canonical 48 kHz time base.
func NewPlanner() (*Planner, error) {
	tb, err := clock.NewTimebase(TargetRate)
	if err != nil {
		return nil, err
	}
	tb.SetEpoch(0)
	return &Planner{tb: tb}, nil
}

// Place records one segment's position using its actual PTS. Positioning is a
// pure function of the PTS, so it is safe to call from concurrent workers in
// any completion order. Gap derivation happens later over the sorted output,
// so no "last seen" state is required here.
func (p *Planner) Place(seg Segment, samples int) (Placement, error) {
	startSample := p.tb.NanosToSample(seg.StartNanos)
	pl := Placement{
		SegmentID:   seg.ID,
		PID:         seg.PID,
		StartSample: startSample,
		StartNanos:  seg.StartNanos,
		Samples:     samples,
	}
	p.placed = append(p.placed, pl)
	return pl, nil
}

// Gaps derives silence spans from the sorted placements: any interval between
// the end of one segment and the start of the next is a gap the muxer must
// bridge. It is computed once, after all (possibly out-of-order) Place calls.
func (p *Planner) Gaps() []Span {
	ordered := p.Placements()
	var spans []Span
	for i := 1; i < len(ordered); i++ {
		prevEnd := ordered[i-1].StartSample + int64(ordered[i-1].Samples)
		nextStart := ordered[i].StartSample
		if nextStart > prevEnd {
			spans = append(spans, Span{
				FromSample: prevEnd,
				ToSample:   nextStart,
				FromNanos:  p.tb.SampleToNanos(prevEnd),
				ToNanos:    p.tb.SampleToNanos(nextStart),
				Code:       clock.CodeRTPLost,
			})
		}
	}
	return spans
}

// Placements returns placements sorted by start sample (VFR order by real PTS).
func (p *Planner) Placements() []Placement {
	out := make([]Placement, len(p.placed))
	copy(out, p.placed)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartSample != out[j].StartSample {
			return out[i].StartSample < out[j].StartSample
		}
		if out[i].PID != out[j].PID {
			return out[i].PID < out[j].PID
		}
		return out[i].SegmentID < out[j].SegmentID
	})
	return out
}

// Spans returns the silence gaps in timeline order, derived from the sorted
// placements at call time.
func (p *Planner) Spans() []Span { return p.Gaps() }

// TotalSamples is the highest sample index reached, including gaps. It is
// computed from the sorted placements, independent of completion order.
func (p *Planner) TotalSamples() int64 {
	ordered := p.Placements()
	if len(ordered) == 0 {
		return 0
	}
	last := ordered[len(ordered)-1]
	return last.StartSample + int64(last.Samples)
}

// TimeBase exposes the shared canonical time base.
func (p *Planner) TimeBase() *clock.Timebase { return p.tb }
