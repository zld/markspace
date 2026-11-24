package ts

import (
	"fmt"

	"mediarun/internal/clock"
)

// CCState is the per-PID continuity-counter state machine.
type CCState struct {
	lastCC    uint8
	hasLast   bool
	Duplicate int
	Gap       int
}

// ContinuityChecker validates 4-bit continuity counters per PID.
// Duplicate packets are reported and flagged for the caller to ignore;
// discontinuities are reported with their exact byte range.
type ContinuityChecker struct {
	states map[uint16]*CCState
	tracks map[uint16]string
}

// NewContinuityChecker creates a checker. tracks maps PID -> audio|video|unknown
// and is used to annotate which track an event affects.
func NewContinuityChecker(tracks map[uint16]string) *ContinuityChecker {
	return &ContinuityChecker{states: map[uint16]*CCState{}, tracks: tracks}
}

// Observe feeds one packet. It returns true when the packet is a duplicate and
// must be ignored by downstream processing.
func (c *ContinuityChecker) Observe(p *Packet, rec *clock.Recorder) (duplicate bool) {
	// Only packets carrying a payload advance the continuity counter.
	if !p.HasPayload {
		return false
	}
	s, ok := c.states[p.PID]
	if !ok {
		s = &CCState{}
		c.states[p.PID] = s
	}
	defer func() { s.lastCC, s.hasLast = p.CC, true }()
	if !s.hasLast {
		return false
	}
	track := c.tracks[p.PID]
	if track == "" {
		track = "unknown"
	}
	if p.CC == s.lastCC {
		s.Duplicate++
		rec.Add(clock.Event{
			Kind: "cc_dup", Severity: "info", PID: p.PID, Track: track,
			Range:  clock.ByteRange{Start: p.Offset, End: p.Offset + PacketSize},
			Detail: fmt.Sprintf("duplicate cc=%d, packet ignored", p.CC),
		})
		return true
	}
	want := (s.lastCC + 1) & 0x0f
	if p.CC != want {
		missing := int(p.CC - want)
		if missing < 0 {
			missing += 16
		}
		s.Gap += missing
		rec.Add(clock.Event{
			Kind: "cc_gap", Severity: "warning", PID: p.PID, Track: track,
			Range:  clock.ByteRange{Start: p.Offset, End: p.Offset + PacketSize},
			Detail: fmt.Sprintf("cc jumped %d -> %d, %d packet(s) lost", s.lastCC, p.CC, missing),
		})
	}
	return false
}

// State returns a snapshot copy of the per-PID counters.
func (c *ContinuityChecker) State() map[uint16]CCState {
	out := make(map[uint16]CCState, len(c.states))
	for pid, s := range c.states {
		out[pid] = *s
	}
	return out
}

// TotalGap sums missing payload packets across all PIDs.
func (c *ContinuityChecker) TotalGap() int {
	n := 0
	for _, s := range c.states {
		n += s.Gap
	}
	return n
}
