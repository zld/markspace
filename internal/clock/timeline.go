package clock

import (
	"math"
)

// Clock rates used by the kernel. RTP audio rates equal the audio sample
// rate; both are expressed in ticks per second.
const (
	NanosPerSecond int64 = 1_000_000_000
	maxInt64             = uint64(math.MaxInt64)
)

// streamState tracks one RTP synchronization source across out-of-order
// packets and 32-bit timestamp wrap-around.
type streamState struct {
	ssrc      uint32
	baseRaw   uint32
	baseSet   bool
	lastRaw   uint32
	wrapCount uint32
	baseNanos int64
	clockRate uint32
	observed  int
}

// Timeline converts ticks from one or more RTP clock domains and from audio
// sample positions onto a single integer-nanosecond media time base. The
// zero value is not usable: construct with NewTimeline.
type Timeline struct {
	rateBySSRC map[uint32]uint32
	streams    map[uint32]*streamState
	// sampleRate is the WAV audio rate, validated against RTP rates by meta.
	sampleRate uint32
}

// NewTimeline creates an empty timeline. Pass zero sampleRate when no WAV
// sidecar is involved.
func NewTimeline(sampleRate uint32) *Timeline {
	return &Timeline{
		rateBySSRC: make(map[uint32]uint32),
		streams:    make(map[uint32]*streamState),
		sampleRate: sampleRate,
	}
}

// RegisterStream binds an SSRC to its RTP clock rate. A zero rate is an
// invalid configuration rather than a packet fault.
func (t *Timeline) RegisterStream(ssrc uint32, clockRate uint32) error {
	if clockRate == 0 {
		return NewError("clock", CatInvalidParam,
			"illegal rtp clock rate 0 for ssrc=%d", ssrc)
	}
	if r, ok := t.rateBySSRC[ssrc]; ok && r != clockRate {
		return NewError("clock", CatLayoutMismatch,
			"ssrc=%d clock rate changed %d -> %d", ssrc, r, clockRate)
	}
	t.rateBySSRC[ssrc] = clockRate
	if _, ok := t.streams[ssrc]; !ok {
		t.streams[ssrc] = &streamState{ssrc: ssrc, clockRate: clockRate}
	}
	return nil
}

// ticksToNanos performs the integer conversion ticks*1e9/rate with explicit
// overflow detection, instead of relying on silent int64 wrap-around.
func ticksToNanos(ticks int64, rate uint32) (int64, error) {
	u := uint64(ticks)
	if ticks < 0 {
		u = uint64(-ticks)
	}
	if u > maxInt64/uint64(NanosPerSecond) {
		return 0, NewError("clock", CatOverflow,
			"timestamp ticks=%d overflow nanosecond conversion", ticks)
	}
	scaled := u * uint64(NanosPerSecond)
	if scaled > maxInt64 {
		return 0, NewError("clock", CatOverflow,
			"timestamp ticks=%d scaled value overflow", ticks)
	}
	n := int64(scaled / uint64(rate))
	if ticks < 0 {
		n = -n
	}
	return n, nil
}

// ObserveRTP maps an RTP packet timestamp onto media nanoseconds. The first
// packet of each SSRC defines origin zero. Forward 2^32 wrap increments the
// wrap counter; small backwards deltas (reordered packets) resolve against
// the previous origin and never move it.
func (t *Timeline) ObserveRTP(ssrc uint32, raw uint32, seq uint16) (int64, error) {
	st, ok := t.streams[ssrc]
	if !ok {
		rate, known := t.rateBySSRC[ssrc]
		if !known {
			return 0, NewError("clock", CatUnmappableTime,
				"ssrc=%d has no registered clock rate", ssrc).WithSeq(seq)
		}
		st = &streamState{ssrc: ssrc, clockRate: rate}
		t.streams[ssrc] = st
	}
	if !st.baseSet {
		st.baseRaw = raw
		st.lastRaw = raw
		st.baseSet = true
		st.observed = 1
		return st.baseNanos, nil
	}

	delta := int64(raw) - int64(st.lastRaw)
	switch {
	case delta < 0 && delta > -(1<<31):
		// Reordered/duplicate packet: resolve relative to the fixed origin.
	case delta < 0:
		// A backwards jump past half the 32-bit space is a forward wrap.
		st.wrapCount++
		st.lastRaw = raw
	default:
		st.lastRaw = raw
	}
	st.observed++

	// Position relative to the origin, including any completed wraps.
	rel := int64(st.wrapCount)*int64(1<<32) + int64(raw) - int64(st.baseRaw)
	if rel < 0 {
		return 0, NewError("clock", CatUnmappableTime,
			"ssrc=%d packet maps before timeline origin", ssrc).WithSeq(seq)
	}
	n, err := ticksToNanos(rel, st.clockRate)
	if err != nil {
		if ce, ok := AsError(err); ok {
			return 0, ce.WithSeq(seq)
		}
		return 0, err
	}
	return st.baseNanos + n, nil
}

// SampleToNanos maps a WAV sample position to media nanoseconds using the
// validated audio sample rate.
func (t *Timeline) SampleToNanos(sampleIndex int64) (int64, error) {
	if t.sampleRate == 0 {
		return 0, NewError("clock", CatInvalidParam,
			"timeline has no audio sample rate")
	}
	if sampleIndex < 0 {
		return 0, NewError("clock", CatInvalidParam,
			"negative sample index %d", sampleIndex)
	}
	return ticksToNanos(sampleIndex, t.sampleRate)
}

// NanosToSample rounds a nanosecond position down to a complete sample.
func (t *Timeline) NanosToSample(nanos int64) (int64, error) {
	if t.sampleRate == 0 {
		return 0, NewError("clock", CatInvalidParam,
			"timeline has no audio sample rate")
	}
	if nanos < 0 {
		return 0, NewError("clock", CatInvalidParam,
			"negative media time %d", nanos)
	}
	return nanos * int64(t.sampleRate) / NanosPerSecond, nil
}

// SampleRate returns the configured audio sample rate (0 when absent).
func (t *Timeline) SampleRate() uint32 { return t.sampleRate }
