// Package repair turns parsed TS packets into a clean stream: continuity
// counters are validated and duplicates ignored, and 33-bit PCR/PTS values
// are corrected into a monotonic per-track timeline. Every repair keeps the
// original raw value alongside the corrected one so downstream nodes can
// audit the decision, and the logic is deterministic for reruns.
package repair

import (
	"github.com/example/tsrepair/internal/model"
	"github.com/example/tsrepair/internal/ts"
)

// GapEvent records one continuity-counter jump with its input byte range.
type GapEvent struct {
	PID         uint16
	ExpectedCC  byte
	GotCC       byte
	Missing     int // number of missing packets
	ByteOffset  int64
	ByteRange   [2]int64 // [offset of last good, offset of jump packet]
}

// DuplicateEvent records an ignored duplicate packet.
type DuplicateEvent struct {
	PID        uint16
	CC         byte
	ByteOffset int64
}

// ContinuityStats summarizes what the checker saw on one stream.
type ContinuityStats struct {
	Gaps        []GapEvent
	Duplicates  []DuplicateEvent
	Checked     int
	NullSkipped int
}

// ContinuityChecker keeps per-PID continuity state.
type ContinuityChecker struct {
	lastCC      map[uint16]byte
	lastOffset  map[uint16]int64
	have        map[uint16]bool
	stats       ContinuityStats
}

// NewContinuityChecker returns an empty checker.
func NewContinuityChecker() *ContinuityChecker {
	return &ContinuityChecker{
		lastCC:     map[uint16]byte{},
		lastOffset: map[uint16]int64{},
		have:       map[uint16]bool{},
	}
}

// CheckResult tells the caller what to do with a packet.
type CheckResult int

const (
	// CheckPass means the packet advances the stream normally.
	CheckPass CheckResult = iota
	// CheckDuplicate means the packet repeats the previous CC and is ignored.
	CheckDuplicate
	// CheckGap means one or more packets were skipped; the packet itself is
	// still delivered so processing resumes at this boundary.
	CheckGap
)

// Check validates one packet. Null packets and payload-less packets are not
// continuity-checked. An adaptation-field discontinuity indicator resets the
// expectation instead of raising a gap.
func (c *ContinuityChecker) Check(p *ts.Packet) (CheckResult, *GapEvent) {
	if p.IsNull() {
		c.stats.NullSkipped++
		return CheckPass, nil
	}
	if !p.HasPayload() {
		// adaptation-only packets do not increment the continuity counter.
		return CheckPass, nil
	}
	if p.Adaptation != nil && p.Adaptation.Discontinuity {
		c.have[p.PID] = true
		c.lastCC[p.PID] = p.ContinuityCounter
		c.lastOffset[p.PID] = p.ByteOffset
		return CheckPass, nil
	}
	c.stats.Checked++
	if !c.have[p.PID] {
		c.have[p.PID] = true
		c.lastCC[p.PID] = p.ContinuityCounter
		c.lastOffset[p.PID] = p.ByteOffset
		return CheckPass, nil
	}
	prev := c.lastCC[p.PID]
	switch {
	case p.ContinuityCounter == prev:
		c.stats.Duplicates = append(c.stats.Duplicates, DuplicateEvent{
			PID: p.PID, CC: p.ContinuityCounter, ByteOffset: p.ByteOffset})
		return CheckDuplicate, nil
	case p.ContinuityCounter == (prev+1)&0x0F:
		// normal increment
	default:
		missing := int((p.ContinuityCounter - prev) & 0x0F)
		ev := GapEvent{
			PID: p.PID, ExpectedCC: (prev + 1) & 0x0F, GotCC: p.ContinuityCounter,
			Missing: missing, ByteOffset: p.ByteOffset,
			ByteRange: [2]int64{c.lastOffset[p.PID], p.ByteOffset},
		}
		c.stats.Gaps = append(c.stats.Gaps, ev)
		c.lastCC[p.PID] = p.ContinuityCounter
		c.lastOffset[p.PID] = p.ByteOffset
		return CheckGap, &ev
	}
	c.lastCC[p.PID] = p.ContinuityCounter
	c.lastOffset[p.PID] = p.ByteOffset
	return CheckPass, nil
}

// Stats returns a copy of the accumulated statistics.
func (c *ContinuityChecker) Stats() ContinuityStats {
	out := c.stats
	out.Gaps = append([]GapEvent(nil), c.stats.Gaps...)
	out.Duplicates = append([]DuplicateEvent(nil), c.stats.Duplicates...)
	return out
}

// FatalMappingError wraps a missing-PID condition into a classified fatal
// error with stage and offset.
func FatalMappingError(p *ts.Packet) *model.StreamError {
	return model.New(model.StageContinuity, model.SeverityFatal,
		"packet PID absent from active PSI mapping").
		Wrap(model.ErrUnknownMapping).At(p.PID, p.ByteOffset)
}
