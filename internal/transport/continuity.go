package transport

// PIDContinuity holds continuity anomalies observed on one PID.
type PIDContinuity struct {
	Duplicates      int
	Missing         int
	Discontinuities int
}

type pidState struct {
	hasLast        bool
	lastCC         uint8
	lastHadPayload bool
}

// ContinuityTracker tracks the 4-bit continuity counter per PID.
// Only packets carrying payload must advance the counter.
type ContinuityTracker struct {
	states map[uint16]*pidState
	ByPID  map[uint16]*PIDContinuity
	Totals PIDContinuity
}

// NewContinuityTracker builds an empty tracker.
func NewContinuityTracker() *ContinuityTracker {
	return &ContinuityTracker{
		states: map[uint16]*pidState{},
		ByPID:  map[uint16]*PIDContinuity{},
	}
}

// Observe updates continuity state for one packet. Packets flagged with
// the transport error indicator and null packets are ignored.
func (t *ContinuityTracker) Observe(p *Packet) {
	if p.TransportError || p.PID == NullPID {
		return
	}
	st := t.states[p.PID]
	if st == nil {
		st = &pidState{}
		t.states[p.PID] = st
	}
	stats := t.ByPID[p.PID]
	if stats == nil {
		stats = &PIDContinuity{}
		t.ByPID[p.PID] = stats
	}
	if p.DiscontinuityIndicator {
		stats.Discontinuities++
		t.Totals.Discontinuities++
		st.hasLast = false // discontinuity flag resets the baseline
	}
	if !p.HasPayload {
		return // adaptation-field-only packets keep the counter frozen
	}
	if !st.hasLast {
		st.hasLast = true
		st.lastCC = p.ContinuityCounter
		st.lastHadPayload = true
		return
	}
	if st.lastHadPayload {
		expected := (st.lastCC + 1) & 0x0F
		switch p.ContinuityCounter {
		case expected:
		case st.lastCC:
			stats.Duplicates++
			t.Totals.Duplicates++
			return // a repeated packet does not move the baseline
		default:
			missing := int((p.ContinuityCounter - expected) & 0x0F)
			stats.Missing += missing
			t.Totals.Missing += missing
		}
	}
	st.lastCC = p.ContinuityCounter
	st.lastHadPayload = true
}
