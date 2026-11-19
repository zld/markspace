package metrics

import "sort"

// Observe classifies one packet. hasPayload and afOnly come straight from the
// TS header; tei is the transport_error_indicator.
//
// Rules applied, in order:
//   - A transport error packet is counted and resets continuity state: the
//     following packet cannot be judged.
//   - Adaptation-field-only packets must not increment the counter. If they
//     do, that is a jump and resets state.
//   - A discontinuity_indicator makes the next counter value arbitrary; the
//     packet is legal and re-anchors tracking.
//   - For payload packets the expected next counter is cc+1 mod 16. Equal
//     counters are a duplicate; anything else is a jump.
func (t *Tracker) Observe(pid uint16, cc uint8, hasPayload, afOnly, discontinuity, tei bool) Verdict {
	s := t.Stats(pid)
	s.Packets++
	t.Total++
	if afOnly {
		s.AFOnly++
	}
	switch {
	case tei:
		s.TransportErrors++
		s.DiscontinuitySeen = true
		s.HasCC = false
		s.broken = true
		return VerdictTransportError
	case afOnly:
		if discontinuity {
			s.DiscontinuitySeen = true
			s.HasCC = false
			s.broken = true
			return VerdictDiscontinuity
		}
		if !s.HasCC {
			s.LastCC, s.HasCC = cc, true
			return VerdictAFOnly
		}
		if cc != s.LastCC {
			s.Jumps++
			s.DiscontinuitySeen = true
			s.LastCC = cc
			return VerdictJump
		}
		s.LastCC = cc
		return VerdictAFOnly
	case !hasPayload:
		// No payload and no adaptation field cannot happen in valid TS; treat
		// it as unjudgeable rather than inventing continuity.
		s.UnknownGaps++
		s.DiscontinuitySeen = true
		s.HasCC = false
		return VerdictUnjudgeable
	}

	if discontinuity {
		s.DiscontinuitySeen = true
		s.LastCC, s.HasCC = cc, true
		s.broken = true
		return VerdictDiscontinuity
	}
	if s.broken {
		// The first payload after damage or an announced discontinuity may
		// have skipped an unknown number of packets; it re-anchors tracking
		// but the gap itself is unjudgeable.
		s.broken = false
		s.UnknownGaps++
		s.LastCC, s.HasCC = cc, true
		return VerdictUnjudgeable
	}
	if !s.HasCC {
		s.LastCC, s.HasCC = cc, true
		return VerdictFirst
	}
	want := (s.LastCC + 1) & 0x0f
	v := VerdictOK
	switch {
	case cc == want:
	case cc == s.LastCC:
		s.Duplicates++
		v = VerdictDuplicate
	default:
		s.Jumps++
		s.DiscontinuitySeen = true
		v = VerdictJump
	}
	s.LastCC = cc
	return v
}

// PIDs returns all tracked PIDs in ascending order.
func (t *Tracker) PIDs() []uint16 {
	out := make([]uint16, 0, len(t.pids))
	for pid := range t.pids {
		out = append(out, pid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Get returns statistics for pid without creating them.
func (t *Tracker) Get(pid uint16) (PIDStats, bool) {
	s, ok := t.pids[pid]
	if !ok {
		return PIDStats{}, false
	}
	return *s, true
}

// Totals aggregates anomaly counters over every PID.
type Totals struct {
	Packets         int64
	Duplicates      int64
	Jumps           int64
	TransportErrors int64
	AFOnly          int64
	UnknownGaps     int64
	Discontinuities int64
}

// Totals sums the per-PID counters.
func (t *Tracker) Totals() Totals {
	var out Totals
	for _, s := range t.pids {
		out.Packets += s.Packets
		out.Duplicates += s.Duplicates
		out.Jumps += s.Jumps
		out.TransportErrors += s.TransportErrors
		out.AFOnly += s.AFOnly
		out.UnknownGaps += s.UnknownGaps
		if s.DiscontinuitySeen {
			out.Discontinuities++
		}
	}
	return out
}
