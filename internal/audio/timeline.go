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
	tb       *clock.Timebase
	placed   []Placement
	spans    []Span
	lastNS   int64
	lastSamp int64
	started  bool
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

// Place records one segment's position using its actual PTS. Backwards time is
// rejected via the shared time base; forward gaps are emitted as silence spans.
func (p *Planner) Place(seg Segment, samples int) (Placement, error) {
	startSample := p.tb.NanosToSample(seg.StartNanos)
	if p.started && seg.StartNanos < p.lastNS {
		return Placement{}, clock.New(clock.CodeTimeBackwards, "audio",
			-1, p.lastNS, seg.StartNanos,
			"segment %d PTS %d is before last %d", seg.ID, p.lastNS, seg.StartNanos)
	}
	if p.started && startSample > p.lastSamp {
		p.spans = append(p.spans, Span{
			FromSample: p.lastSamp,
			ToSample:   startSample,
			FromNanos:  p.tb.SampleToNanos(p.lastSamp),
			ToNanos:    seg.StartNanos,
			Code:       clock.CodeRTPLost,
		})
	}
	pl := Placement{
		SegmentID:   seg.ID,
		PID:         seg.PID,
		StartSample: startSample,
		StartNanos:  seg.StartNanos,
		Samples:     samples,
	}
	p.placed = append(p.placed, pl)
	p.lastNS = seg.StartNanos
	p.lastSamp = startSample + int64(samples)
	p.started = true
	return pl, nil
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

// Spans returns the silence gaps in timeline order.
func (p *Planner) Spans() []Span {
	out := make([]Span, len(p.spans))
	copy(out, p.spans)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].FromSample < out[j].FromSample
	})
	return out
}

// TotalSamples is the highest sample index reached, including gaps.
func (p *Planner) TotalSamples() int64 { return p.lastSamp }

// TimeBase exposes the shared canonical time base.
func (p *Planner) TimeBase() *clock.Timebase { return p.tb }
