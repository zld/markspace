// Package pipeline fans the parsed representations out for independent
// recovery and fans them back in on a shared clock for boundary cross-checks.
// Recovery state is strictly per representation: a timestamp synthesized for
// one bitrate tier is never written into another tier.
package pipeline

import (
	"rtpreplay/clock"
	"rtpreplay/ingest"
	"rtpreplay/media"
)

// DefaultMaxGap bounds the consecutive missing packets that interpolation
// will repair; longer holes stay unrecoverable and are reported.
const DefaultMaxGap = 16

// Kind strings for the two media families.
const (
	KindAudio = "audio"
	KindVideo = "video"
)

// Boundary is a candidate cut point located on actual sample times. It marks
// the first sample of the span that follows; span closure is derived by the
// segment package from consecutive boundaries.
type Boundary struct {
	TimeMS      float64
	FirstRecord int
	ExtSeqStart uint64
}

// RecoveredPacket is a synthesized packet filling a sequence hole.
type RecoveredPacket struct {
	ExtSeq     uint64
	TimeMS     float64
	BetweenRec [2]int // record indices of the bracketing observed packets
}

// Representation is one bitrate tier after independent recovery.
type Representation struct {
	BitrateKbps int
	Kind        string
	Stream      *ingest.Stream

	Missing       []uint64 // extended sequence numbers absent from the input
	Recovered     []RecoveredPacket
	Unrecoverable []uint64

	// Boundaries in shared-clock milliseconds.
	Boundaries []Boundary
}

// RecoveredCount / MissingCount support rate reporting.
func (r *Representation) RecoveredCount() int { return len(r.Recovered) }
func (r *Representation) MissingCount() int   { return len(r.Missing) }

// Fanout builds one independently recovered Representation per input stream.
func Fanout(streams []*ingest.Stream, kind string, maxGap int) ([]*Representation, error) {
	if maxGap <= 0 {
		maxGap = DefaultMaxGap
	}
	out := make([]*Representation, 0, len(streams))
	for _, st := range streams {
		rep, err := recoverOne(st, kind, maxGap)
		if err != nil {
			return nil, err
		}
		out = append(out, rep)
	}
	return out, nil
}

func recoverOne(st *ingest.Stream, kind string, maxGap int) (*Representation, error) {
	if !media.SupportedClockRate(st.ClockRate) {
		return nil, media.NewError(media.KindUnsupported, "UNSUPPORTED_RATE", "pipeline", -1,
			"rate %d outside sample scope", st.ClockRate)
	}
	rep := &Representation{
		BitrateKbps: st.BitrateKbps,
		Kind:        kind,
		Stream:      st,
	}
	pkts := st.Packets
	for i := 1; i < len(pkts); i++ {
		prev, cur := pkts[i-1], pkts[i]
		gap := cur.Clock.ExtSeq - prev.Clock.ExtSeq - 1
		if gap == 0 {
			continue
		}
		hole := make([]uint64, 0, gap)
		for s := prev.Clock.ExtSeq + 1; s < cur.Clock.ExtSeq; s++ {
			hole = append(hole, s)
		}
		rep.Missing = append(rep.Missing, hole...)

		reparable := !prev.Clock.Isolated && !cur.Clock.Isolated &&
			cur.Clock.Anomaly != clock.AnomSeqRestart &&
			int(gap) <= maxGap
		if !reparable {
			rep.Unrecoverable = append(rep.Unrecoverable, hole...)
			continue
		}
		// Linear interpolation on the representation's own unwrapped clock;
		// no other representation's state is read or modified.
		span := cur.Clock.Unwrapped - prev.Clock.Unwrapped
		step := float64(span) / float64(gap+1)
		for j, s := range hole {
			ticks := float64(prev.Clock.Unwrapped) + step*float64(j+1)
			rep.Recovered = append(rep.Recovered, RecoveredPacket{
				ExtSeq:     s,
				TimeMS:     media.TicksToMS(int64(ticks), st.ClockRate),
				BetweenRec: [2]int{prev.RecordIndex, cur.RecordIndex},
			})
		}
	}
	return rep, nil
}

// LossRecoveryRate is recovered / missing; 1.0 when nothing was missing.
func LossRecoveryRate(reps []*Representation) float64 {
	missing, recovered := 0, 0
	for _, r := range reps {
		missing += r.MissingCount()
		recovered += r.RecoveredCount()
	}
	if missing == 0 {
		return 1
	}
	return float64(recovered) / float64(missing)
}

// LocateBoundaries places a boundary at the stream start and then every
// periodMS. For variable frame rates each target snaps to the observed
// sample whose time is closest instead of assuming a fixed frame interval.
func (r *Representation) LocateBoundaries(periodMS float64) {
	if periodMS <= 0 || len(r.Stream.Packets) == 0 {
		return
	}
	rate := r.Stream.ClockRate
	pkts := r.Stream.Packets
	timeAt := func(i int) float64 {
		return media.TicksToMS(pkts[i].Clock.Unwrapped, rate)
	}
	firstMS := timeAt(0)
	lastMS := timeAt(len(pkts) - 1)

	r.Boundaries = append(r.Boundaries, Boundary{
		TimeMS: firstMS, FirstRecord: pkts[0].RecordIndex, ExtSeqStart: pkts[0].Clock.ExtSeq,
	})
	target := firstMS + periodMS
	idx := 1
	for target <= lastMS {
		// Find the two samples bracketing the target and snap to the nearer.
		for idx < len(pkts)-1 && timeAt(idx) < target {
			idx++
		}
		before := idx - 1
		if before < 0 {
			before = 0
		}
		chosen := before
		if abs64(timeAt(idx)-target) < abs64(target-timeAt(before)) {
			chosen = idx
		}
		p := pkts[chosen]
		if len(r.Boundaries) > 0 && r.Boundaries[len(r.Boundaries)-1].ExtSeqStart == p.Clock.ExtSeq {
			target += periodMS
			continue // two targets snapped to the same sample
		}
		r.Boundaries = append(r.Boundaries, Boundary{
			TimeMS: timeAt(chosen), FirstRecord: p.RecordIndex, ExtSeqStart: p.Clock.ExtSeq,
		})
		target += periodMS
	}
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
