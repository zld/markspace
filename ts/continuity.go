package ts

import (
	"medkernel/media"
)

// ContinuityEvent describes one CC anomaly on a PID.
type ContinuityEvent struct {
	PID       uint16
	Index     int64
	ByteOff   int64
	ByteEnd   int64
	Kind      media.Class // ClassContinuity for dup/gap; ClassTruncated for cuts
	CC        uint8
	Expected  uint8
	Duplicate bool
	Gap       int // number of missing CC steps (1 = single lost packet)
}

// ContinuityStats counts per-PID and aggregate anomalies.
type ContinuityStats struct {
	Duplicates  int64
	Gaps        int64
	Recovered   int64 // gaps later seen filled (adaptation discontinuity flag set)
	Unrecovered int64
}

type ccState struct {
	cc      uint8
	has     bool
	lastIdx int64
}

// ContinuityChecker validates the 4-bit continuity counter per PID.
// Duplicate consecutive packets are flagged and expected to be ignored;
// CC jumps are classified as gaps with the missing step count recorded.
type ContinuityChecker struct {
	states map[uint16]*ccState
	Events []ContinuityEvent
	Stats  ContinuityStats
}

// NewContinuityChecker builds an empty checker.
func NewContinuityChecker() *ContinuityChecker {
	return &ContinuityChecker{states: make(map[uint16]*ccState)}
}

// Check evaluates one packet. It returns the event (with Duplicate=true for a
// repeated CC) and true when an anomaly was detected. Packets carrying neither
// payload nor adaptation field do not increment the CC and are skipped.
func (c *ContinuityChecker) Check(p Packet) (ContinuityEvent, bool) {
	if !p.HasPayload && !p.HasAdaptation {
		return ContinuityEvent{}, false
	}
	st := c.states[p.PID]
	if st == nil {
		st = &ccState{}
		c.states[p.PID] = st
	}
	if !st.has {
		st.cc, st.has, st.lastIdx = p.CC, true, p.Index
		return ContinuityEvent{}, false
	}
	expected := (st.cc + 1) & 0x0F
	if p.CC == st.cc && !p.Discontinuity() {
		// Repeated CC without an explicit discontinuity: duplicate packet.
		ev := ContinuityEvent{
			PID: p.PID, Index: p.Index, ByteOff: p.Offset,
			ByteEnd: p.Offset + int64(p.Size), Kind: media.ClassContinuity,
			CC: p.CC, Expected: expected, Duplicate: true,
		}
		c.Stats.Duplicates++
		c.Events = append(c.Events, ev)
		return ev, true // baseline CC does not move on a duplicate
	}
	anomaly := false
	ev := ContinuityEvent{
		PID: p.PID, Index: p.Index, ByteOff: p.Offset,
		ByteEnd: p.Offset + int64(p.Size), CC: p.CC, Expected: expected,
	}
	if p.CC != expected {
		gap := (int(p.CC-st.cc) & 0x0F) - 1
		if gap < 0 {
			gap += 16
		}
		ev.Kind, ev.Gap, anomaly = media.ClassContinuity, gap, true
		c.Stats.Gaps++
		if p.Discontinuity() {
			c.Stats.Recovered++ // signalled, intentional discontinuity
		} else {
			c.Stats.Unrecovered++
		}
		c.Events = append(c.Events, ev)
	}
	st.cc, st.lastIdx = p.CC, p.Index
	return ev, anomaly
}

// Reset clears all per-PID state (used at PSI track-map boundaries).
func (c *ContinuityChecker) Reset() {
	c.states = make(map[uint16]*ccState)
}
