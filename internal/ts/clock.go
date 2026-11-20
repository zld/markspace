package ts

import "fmt"

// PTSModulus is 2^33, the period of the 33-bit MPEG presentation timestamp.
const PTSModulus int64 = 1 << 33

// PTSHalfModulus is half the period; deltas with this magnitude are treated
// as backward wraparound rather than forward jumps.
const PTSHalfModulus int64 = 1 << 32

// PTSUnwrapper expands modular 33-bit PTS values onto a monotonically
// increasing int64 timeline, transparently handling 2^33 wraparound.
type PTSUnwrapper struct {
	last      int64 // last raw 33-bit value
	unwrapped int64 // expanded value corresponding to last
	started   bool
}

// Push feeds a raw 33-bit PTS and returns its unwrapped value. The first
// sample establishes the origin; later samples are joined by the shortest
// signed modular delta, so both forward progress and wraparound are handled.
func (u *PTSUnwrapper) Push(pts33 int64) int64 {
	pts33 &= PTSModulus - 1
	if !u.started {
		u.started = true
		u.last = pts33
		u.unwrapped = pts33
		return pts33
	}
	delta := pts33 - u.last
	if delta > PTSHalfModulus {
		delta -= PTSModulus // backward wrap seen as raw value reset to low range
	} else if delta < -PTSHalfModulus {
		delta += PTSModulus // forward wrap: raw value dropped from high to low
	}
	u.unwrapped += delta
	u.last = pts33
	return u.unwrapped
}

// CCEvent classifies a packet's continuity counter relative to its PID flow.
type CCEvent int

const (
	// CCOk means the counter advanced by exactly one as required.
	CCOk CCEvent = iota
	// CCDuplicate means the same counter repeated with a payload, i.e. a
	// retransmitted packet rather than a counter increment.
	CCDuplicate
	// CCDeclared means the adaptation field explicitly set discontinuity.
	CCDeclared
	// CCJump means the counter changed without a declared discontinuity.
	CCJump
)

func (e CCEvent) String() string {
	switch e {
	case CCDuplicate:
		return "duplicate"
	case CCDeclared:
		return "declared_discontinuity"
	case CCJump:
		return "undeclared_jump"
	default:
		return "ok"
	}
}

// ccState is the per-PID continuity bookkeeping.
type ccState struct {
	lastCC  int
	hasLast bool
	// lastCarriedPayload remembers whether the previous packet had a payload;
	// adaptation-field-only packets must not increment the counter.
	lastCarriedPayload bool
}

// ContinuityTracker validates 4-bit continuity counters independently per
// PID, distinguishing duplicates, declared discontinuities and raw jumps.
type ContinuityTracker struct {
	states map[int]*ccState
}

// NewContinuityTracker creates an empty tracker.
func NewContinuityTracker() *ContinuityTracker {
	return &ContinuityTracker{states: make(map[int]*ccState)}
}

// Observe classifies one packet and returns the event plus the counter value
// that would have been legal here (meaningful for CCJump; equal to the
// observed value otherwise). The null PID (0x1FFF) has no counter semantics
// and is always reported as CCOk.
func (t *ContinuityTracker) Observe(p *Packet) (CCEvent, int) {
	if p.PID == PIDNull {
		return CCOk, p.ContinuityCounter
	}
	st := t.states[p.PID]
	if st == nil {
		st = &ccState{}
		t.states[p.PID] = st
	}
	carriesPayload := p.AdaptationControl == 1 || p.AdaptationControl == 3

	if p.Discontinuity {
		// A declared discontinuity resets expectations at this point.
		st.hasLast = true
		st.lastCC = p.ContinuityCounter
		st.lastCarriedPayload = carriesPayload
		return CCDeclared, p.ContinuityCounter
	}
	if !st.hasLast {
		st.hasLast = true
		st.lastCC = p.ContinuityCounter
		st.lastCarriedPayload = carriesPayload
		return CCOk, p.ContinuityCounter
	}
	if p.ContinuityCounter == st.lastCC {
		expected := st.lastCC
		if carriesPayload && st.lastCarriedPayload {
			st.lastCarriedPayload = carriesPayload
			return CCDuplicate, expected
		}
		// Repeated counter without payload (pure adaptation field) is legal.
		st.lastCarriedPayload = carriesPayload
		return CCOk, expected
	}
	expected := (st.lastCC + 1) & 0x0F
	st.lastCC = p.ContinuityCounter
	st.lastCarriedPayload = carriesPayload
	if p.ContinuityCounter != expected {
		return CCJump, expected
	}
	return CCOk, expected
}

// CCError describes a continuity failure with enough context to locate it.
type CCError struct {
	PID      int
	Offset   int64
	Got      int
	Expected int
	Kind     CCEvent
}

func (e CCError) Error() string {
	return fmt.Sprintf("continuity %s on PID 0x%04x at offset %d: got cc=%d want cc=%d",
		e.Kind, e.PID, e.Offset, e.Got, e.Expected)
}
