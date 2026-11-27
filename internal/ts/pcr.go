// Package ts: PCR unwrapping and clock conversion.
package ts

import (
	"example.com/assetpack/internal/fault"
)

// checkPCR unwraps 33-bit PCRs.  A wrap is accepted only when the packet is
// trusted (PID continuity held), the discontinuity indicator is clear, the
// observed backwards step exceeds half the modulus, and the resulting time is
// monotonic and advances by a plausible amount.  Everything else is a
// reported discontinuity, never a silent correction.
func (p *parser) checkPCR(pkt *Packet, trusted bool) {
	st := p.state(pkt.PID)
	advance := func(v uint64) int64 { return PCRToNanos(v) }
	if !st.havePCR {
		st.havePCR = true
		st.lastRaw = pkt.PCRRaw
		st.unwrap = pkt.PCRRaw
		st.lastTime = advance(st.unwrap)
		st.mono = st.lastTime
		pkt.PCRUnwrapped = st.unwrap
		pkt.PCRNanos = st.lastTime
		return
	}
	candidate := st.unwrap
	accepted := false
	wrapped := false
	if pkt.PCRRaw < st.lastRaw {
		candidate, wrapped = UnwrapPCR(st.lastRaw, st.unwrap, pkt.PCRRaw, trusted, pkt.Discontinuity)
		accepted = candidate != st.unwrap
		if !accepted {
			p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindPCR,
				"PCR regression on PID %d raw %d -> %d cannot be proven a wrap", pkt.PID, st.lastRaw, pkt.PCRRaw).
				AtOffset(pkt.Offset).AtSample(st.lastTime).Recover(true))
		}
	} else {
		step := pkt.PCRRaw - st.lastRaw
		if step > MaxPCRAdvance && !pkt.Discontinuity {
			p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindPCR,
				"PCR step of %d ticks on PID %d exceeds ten seconds", step, pkt.PID).
				AtOffset(pkt.Offset).AtSample(st.lastTime).Recover(true))
		}
		candidate = st.unwrap + step
		accepted = true
	}
	var nanos int64
	if accepted {
		nanos = advance(candidate)
		if nanos < st.mono {
			// A new raw value that resolves earlier than an already published
			// time breaks monotonicity: publish the retained time instead.
			p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindPCR,
				"PCR time regressed %d -> %d ns on PID %d", st.mono, nanos, pkt.PID).
				AtOffset(pkt.Offset).AtSample(st.mono).Recover(true))
			accepted = false
		}
	}
	if !accepted {
		pkt.PCRUnwrapped = st.unwrap
		pkt.PCRWraps = st.wraps
		pkt.PCRNanos = st.mono
		return
	}
	st.lastRaw = pkt.PCRRaw
	st.unwrap = candidate
	st.lastTime = nanos
	st.mono = nanos
	pkt.PCRUnwrapped = candidate
	pkt.PCRWraps = st.wraps
	pkt.PCRNanos = nanos
	if wrapped {
		st.wraps++
		pkt.PCRWraps = st.wraps
		p.stream.Stats.PCRWraps++
	}
}

// UnwrapPCR resolves a 33-bit raw base against the previous unwrapped value,
// accepting a wrap only under the same proof conditions as packet parsing.
// It is exported for index rebuilders and tests.
func UnwrapPCR(prevRaw, prevUnwrapped, raw uint64, ccOK, discontinuity bool) (uint64, bool) {
	if raw >= prevRaw {
		return prevUnwrapped + (raw - prevRaw), false
	}
	delta := prevRaw - raw
	if !ccOK || discontinuity || delta <= PCRWrapThreshold || delta >= PCRModulus || raw >= PCRWrapThreshold {
		return prevUnwrapped, false
	}
	cand := prevUnwrapped + PCRModulus - delta
	if cand <= prevUnwrapped || cand-prevUnwrapped >= MaxPCRAdvance {
		return prevUnwrapped, false
	}
	return cand, true
}

// PCRToNanos converts an unwrapped 90 kHz PCR base to nanoseconds.
func PCRToNanos(base uint64) int64 { return int64(base) * 1e9 / int64(PCRClockHz) }

// SplitPCR separates a 42-bit PCR value into base and extension.
func SplitPCR(v uint64) (base uint64, ext uint16) {
	return v >> 9, uint16(v & 0x1FF)
}

// CombinePCR joins base and extension.
func CombinePCR(base uint64, ext uint16) uint64 { return base<<9 | uint64(ext&0x1FF) }
