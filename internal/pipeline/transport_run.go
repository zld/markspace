package pipeline

import (
	"mediaroute/internal/clock"
	"mediaroute/internal/timeline"
	"mediaroute/internal/transport"
)

// runTransport pushes every on-wire packet through the demux and reassembles
// released packets into frames, invoking gap handling on detected bursts.
func (r *Runner) runTransport(demux *transport.Demux,
	rass map[uint32]*transport.Reassembler) (map[uint32][]*transport.Frame,
	[]transport.Stats, error) {
	out := map[uint32][]*transport.Frame{}
	prevGapCount := map[uint32]int{}
	for _, p := range r.fx.Packets {
		released, _, err := demux.Push(p)
		if err != nil {
			return nil, nil, err
		}
		ssrc := p.SSRC
		st := demux.Stream(ssrc).Stats()
		// When a new gap appears, terminate the in-flight frame as truncated.
		if len(st.Gaps) > prevGapCount[ssrc] {
			if f := rass[ssrc].Gap(); f != nil {
				out[ssrc] = append(out[ssrc], f)
			}
			prevGapCount[ssrc] = len(st.Gaps)
		}
		for _, rel := range released {
			f, err := rass[ssrc].Push(rel)
			if err != nil {
				return nil, nil, err
			}
			if f != nil {
				out[ssrc] = append(out[ssrc], f)
			}
		}
	}
	// Flush any packets still held inside the reorder window at stream end;
	// Drain also closes terminal gaps so conservation accounting is exact.
	for ssrc, tail := range demux.Drain() {
		if len(demux.Stream(ssrc).Stats().Gaps) > prevGapCount[ssrc] {
			if f := rass[ssrc].Gap(); f != nil {
				out[ssrc] = append(out[ssrc], f)
			}
			prevGapCount[ssrc] = len(demux.Stream(ssrc).Stats().Gaps)
		}
		for _, rel := range tail {
			f, err := rass[ssrc].Push(rel)
			if err != nil {
				return nil, nil, err
			}
			if f != nil {
				out[ssrc] = append(out[ssrc], f)
			}
		}
	}
	// Flush any unterminated frame at stream end as incomplete.
	for ssrc, g := range rass {
		if f := g.Flush(); f != nil {
			out[ssrc] = append(out[ssrc], f)
		}
	}
	var allStats []transport.Stats
	for _, ssrc := range demux.SSRCs() {
		allStats = append(allStats, demux.Stream(ssrc).Stats())
	}
	return out, allStats, nil
}

func (r *Runner) recordTransportStats(stats []transport.Stats) error {
	var accepted, dups, reordered, lost, gaps int
	for _, st := range stats {
		accepted += st.Received
		dups += st.Duplicates
		reordered += st.Reordered
		lost += st.Lost
		gaps += len(st.Gaps)
	}
	// Cross-stage conservation: nothing invented, nothing swallowed.
	if err := timeline.CheckConservation("transport",
		accepted, dups, lost, r.fx.TruthPackets); err != nil {
		return clock.E(clock.PhaseTransport, 0, -1, false,
			"conservation check failed", err)
	}
	r.metrics.Accepted = accepted
	r.metrics.Duplicates = dups
	r.metrics.Reordered = reordered
	r.metrics.Lost = lost
	r.metrics.Gaps = gaps
	r.metrics.TruthPackets = r.fx.TruthPackets
	r.metrics.CongestionPeak = r.fx.CongestionPeak
	return nil
}
