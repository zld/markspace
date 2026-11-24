package clock

import "fmt"

// TicksPerSecond is the shared 90 kHz media clock rate used by every
// subsystem (RTP timestamps, repaired timelines and index entries).
const TicksPerSecond int64 = 90_000

// Tick is one unit of the 90 kHz media clock (1/90000 s).
type Tick int64

// Tick32 is the on-wire 32-bit RTP timestamp representation.
type Tick32 uint32

// Seq16 is the on-wire 16-bit RTP sequence number.
type Seq16 uint16

// Milli converts 90 kHz ticks to milliseconds (exact: 90 ticks = 1 ms).
func (t Tick) Milli() float64 { return float64(t) / 90.0 }

// Seconds converts ticks to seconds.
func (t Tick) Seconds() float64 { return float64(t) / float64(TicksPerSecond) }

// MilliToTick converts a millisecond duration into ticks.
func MilliToTick(ms float64) Tick { return Tick(ms * 90.0) }

// ExtendSeq16 expands a 16-bit rolling sequence number into a monotonically
// increasing 64-bit sequence relative to the previously extended value.
// Forward wraparound (e.g. 0xFFFF -> 0x0000) adds 65536; a value within half
// the number space above prev is treated as forward, the rest as an older
// duplicate/reordered packet and kept below prev.
func ExtendSeq16(prev int64, s Seq16) int64 {
	cycle := prev >> 16
	base := cycle << 16
	ext := base + int64(s)
	delta := ext - prev
	if delta > 32768 { // s is actually in the previous cycle
		ext -= 1 << 16
	} else if delta < -32767 { // s is actually in the next cycle
		ext += 1 << 16
	}
	return ext
}

// ExtendTick32 expands a 32-bit wrapping RTP timestamp into 64-bit ticks
// using the same half-number-space rule as ExtendSeq16.
func ExtendTick32(prev Tick, ts Tick32) Tick {
	ext := Tick(int64(prev&^0xFFFFFFFF) + int64(int32(ts)))
	delta := ext - prev
	if delta > 1<<31 {
		ext -= 1 << 32
	} else if delta < -(1<<31 - 1) {
		ext += 1 << 32
	}
	return ext
}

// SampleConverter maps audio sample counts at a given sample rate onto the
// 90 kHz clock while preserving the fractional remainder. Converting sample
// by sample (or in arbitrary blocks) never loses more than one tick per
// conversion and the accumulated error over an arbitrarily long stream is
// strictly less than one source sample period.
type SampleConverter struct {
	sampleRate int64
	acc        Tick // exact accumulated ticks *before* rational rounding
	whole      Tick // last whole-tick position returned
}

// NewSampleConverter creates a converter for e.g. 48000 Hz PCM.
func NewSampleConverter(sampleRate int) (*SampleConverter, error) {
	if sampleRate <= 0 || sampleRate > 768000 {
		return nil, NewError(ErrInvalidArgument, "unsupported sample rate %d", sampleRate)
	}
	return &SampleConverter{sampleRate: int64(sampleRate)}, nil
}

// SampleRate returns the configured source rate.
func (c *SampleConverter) SampleRate() int { return int(c.sampleRate) }

// TicksFor returns the exact (rational) tick duration of n samples as
// whole ticks plus a remainder in [0,sampleRate). The remainder must be
// carried by the caller's accumulator; this helper performs no state change.
func (c *SampleConverter) TicksFor(n int) (whole Tick, remainder int64) {
	num := int64(n) * TicksPerSecond
	return Tick(num / c.sampleRate), num % c.sampleRate
}

// Advance maps the next n samples onto the shared clock and returns the
// tick position at the start of that block and the tick position just past
// its end. Fractional ticks are retained internally so a long stream drifts
// by less than one source sample period in total.
func (c *SampleConverter) Advance(n int) (start, end Tick) {
	if n <= 0 {
		return c.whole, c.whole
	}
	start = c.whole
	c.acc += Tick(int64(n) * TicksPerSecond)
	c.whole = Tick(int64(c.acc) / c.sampleRate)
	return start, c.whole
}

// Position returns the current whole-tick position.
func (c *SampleConverter) Position() Tick { return c.whole }

// Reset restores the converter to a zero position.
func (c *SampleConverter) Reset() { c.acc, c.whole = 0, 0 }

// SamplesToTick converts an absolute sample index to a tick position using
// exact rational arithmetic (no per-call remainder loss).
func (c *SampleConverter) SamplesToTick(sampleIndex int64) Tick {
	if sampleIndex < 0 {
		panic(fmt.Sprintf("negative sample index %d", sampleIndex))
	}
	return Tick(sampleIndex * TicksPerSecond / c.sampleRate)
}

// TickToSamples returns the sample index whose start interval contains t.
func (c *SampleConverter) TickToSamples(t Tick) int64 {
	return int64(t) * c.sampleRate / TicksPerSecond
}
