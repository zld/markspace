package ts

import "mediad/internal/model"

// ContinuityTracker watches the 4-bit continuity counter of every PID.
// Per ISO 13818-1 a payload-bearing packet must increment the counter by 1
// modulo 16. Packets carrying no payload (adaptation-only) do not increment
// the counter and are ignored; duplicate packets (same counter, payload
// start) are flagged as repeated so callers may safely ignore them.
type ContinuityTracker struct {
	last      map[uint16]uint8
	have      map[uint16]bool
	// Gaps accumulates every observed discontinuity in stream order.
	Gaps []model.Gap
}

// NewContinuityTracker creates an empty tracker.
func NewContinuityTracker() *ContinuityTracker {
	return &ContinuityTracker{
		last: make(map[uint16]uint8),
		have: make(map[uint16]bool),
	}
}

// Observe checks one packet. It returns a non-nil Gap on the first packet of
// a PID only when that packet is malformed relative to the stream; the first
// packet itself simply seeds the expected counter and returns nil.
func (t *ContinuityTracker) Observe(p *model.Packet) *model.Gap {
	// Adaptation-only packets leave the continuity counter untouched.
	if !p.HasPayload {
		return nil
	}
	prevCC, ok := t.last[p.PID]
	if !ok {
		t.have[p.PID] = true
		t.last[p.PID] = p.Continuity
		return nil
	}
	expected := (prevCC + 1) & 0x0F
	if p.Continuity == prevCC {
		gap := model.Gap{
			Offset: p.Offset, PID: p.PID,
			Expected: prevCC, Actual: p.Continuity, Repeated: true,
		}
		t.Gaps = append(t.Gaps, gap)
		return &gap
	}
	if p.Continuity != expected {
		gap := model.Gap{
			Offset: p.Offset, PID: p.PID,
			Expected: expected, Actual: p.Continuity,
		}
		t.Gaps = append(t.Gaps, gap)
		t.last[p.PID] = p.Continuity
		return &gap
	}
	t.last[p.PID] = p.Continuity
	return nil
}

// GapsForPID returns discontinuities affecting one PID.
func (t *ContinuityTracker) GapsForPID(pid uint16) []model.Gap {
	var out []model.Gap
	for i := range t.Gaps {
		if t.Gaps[i].PID == pid {
			out = append(out, t.Gaps[i])
		}
	}
	return out
}
