// Package clock holds the shared integer time base used by every subsystem.
// All inter-module time is expressed as int64 nanoseconds since the trace
// epoch; sample positions use integer sample ticks at a known rate. Floating
// point is never used to place media, so two runs over the same input always
// produce the same timeline.
package clock

import "fmt"

// Fixed clock rates from the relevant specifications.
const (
	NanosPerSecond int64 = 1_000_000_000

	// RTPPacketRate is the MPEG-TS-over-RTP (RFC 2250) media clock rate.
	RTPPacketRate int64 = 90_000
	// RTPWrapTicks is the uint32 RTP timestamp period in 90 kHz ticks.
	RTPWrapTicks int64 = 1 << 32

	// PCRRate is the 27 MHz MPEG-TS program clock.
	PCRRate int64 = 27_000_000
	// PCRBaseMod is the 33-bit PCR base period.
	PCRBaseMod int64 = 1 << 33
	// PCRWrapTicks is the full 42-bit PCR period in 27 MHz ticks.
	PCRWrapTicks int64 = PCRBaseMod * 300

	// MaxSaneBackwardNanos bounds how far a timestamp may jump backwards
	// before it is treated as a clock discontinuity rather than a wrap.
	MaxSaneBackwardNanos int64 = 500_000_000
)

// Timebase maps integer sample ticks at a fixed rate onto the global
// nanosecond timeline and enforces monotonic emission.
type Timebase struct {
	rate  int64
	epoch int64 // nanoseconds corresponding to sample index 0
	last  int64 // last accepted nanosecond value
	set   bool
}

// NewTimebase creates a time base for the given sample rate (samples/s).
func NewTimebase(rate int64) (*Timebase, error) {
	if rate <= 0 {
		return nil, fmt.Errorf("clock: sample rate must be positive, got %d", rate)
	}
	return &Timebase{rate: rate}, nil
}

// Rate returns the sample rate.
func (t *Timebase) Rate() int64 { return t.rate }

// SetEpoch pins the nanosecond instant of sample index 0.
func (t *Timebase) SetEpoch(ns int64) { t.epoch, t.last, t.set = ns, ns, true }

// Epoch returns the nanosecond instant of sample index 0.
func (t *Timebase) Epoch() int64 { return t.epoch }

// SampleToNanos converts a sample index to nanoseconds, rounded to nearest.
func (t *Timebase) SampleToNanos(sample int64) int64 {
	q := sample * NanosPerSecond
	r := q % t.rate
	if r < 0 {
		r = -r
	}
	inc := int64(0)
	if r*2 >= t.rate {
		inc = 1
	}
	ns := t.epoch + q/t.rate
	if sample >= 0 {
		return ns + inc
	}
	return ns - inc
}

// NanosToSample converts a nanosecond instant to a sample index (floor).
func (t *Timebase) NanosToSample(ns int64) int64 {
	d := ns - t.epoch
	q := d / NanosPerSecond
	r := d % NanosPerSecond
	s := q * t.rate
	switch {
	case r == 0:
		return s
	case r > 0:
		return s + (r*t.rate)/NanosPerSecond
	default:
		// negative remainder: floor toward minus infinity
		return s - ((-r)*t.rate+NanosPerSecond-1)/NanosPerSecond
	}
}

// Check rejects timestamps that move backwards beyond the wrap tolerance.
func (t *Timebase) Check(ns int64) error {
	if t.set && ns < t.last-MaxSaneBackwardNanos {
		return New(CodeTimeBackwards, "clock", -1, t.last, ns,
			"timestamp %d ns is before last %d ns", ns, t.last)
	}
	return nil
}

// Advance validates and records the next emitted nanosecond timestamp.
func (t *Timebase) Advance(ns int64) (int64, error) {
	if err := t.Check(ns); err != nil {
		return t.last, err
	}
	if !t.set || ns > t.last {
		t.last = ns
	}
	if !t.set {
		t.set = true
	}
	return t.last, nil
}

// Last returns the most recently accepted nanosecond value.
func (t *Timebase) Last() (int64, bool) { return t.last, t.set }

// RTPTicksToNanos converts an unwrapped 90 kHz RTP timestamp to nanoseconds.
func RTPTicksToNanos(ticks int64) int64 {
	return ticks * NanosPerSecond / RTPPacketRate
}

// NanosToRTPTicks converts nanoseconds to an unwrapped 90 kHz RTP tick count.
func NanosToRTPTicks(ns int64) int64 {
	return ns * RTPPacketRate / NanosPerSecond
}

// PCRToNanos converts an unwrapped 27 MHz PCR tick count to nanoseconds.
func PCRToNanos(pcrTicks int64) int64 {
	return pcrTicks * NanosPerSecond / PCRRate
}

// NanosToPCR converts nanoseconds to unwrapped 27 MHz PCR ticks.
func NanosToPCR(ns int64) int64 {
	return ns * PCRRate / NanosPerSecond
}

// PCRWrapNanos is the duration of one full 42-bit PCR cycle.
func PCRWrapNanos() int64 { return PCRToNanos(PCRWrapTicks) }
