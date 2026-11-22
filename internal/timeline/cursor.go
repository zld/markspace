package timeline

import (
	"fmt"
	"math/bits"
)

// SampleCursor walks an output sample grid while consuming source samples.
// It is the only place resampling decides where an output sample reads from,
// so the resampler cannot invent its own time mapping.
//
// Output sample n of a run reads the source at
//
//	src = SrcStart + n * InRate / OutRate
//
// evaluated from the run origin in exact integer arithmetic. The fractional
// part of that position is the interpolation phase; because it is derived
// from the origin each time, phase error never accumulates across a run.
type SampleCursor struct {
	InRate   int64
	OutRate  int64
	SrcStart int64 // input-sample origin of the run
	OutStart int64 // output-sample origin of the run
	count    int64
}

// NewSampleCursor validates rates and opens a run of outCount output samples.
func NewSampleCursor(inRate, outRate, srcStart, outStart, outCount int64) (SampleCursor, error) {
	if inRate <= 0 || inRate > 1_000_000_000 {
		return SampleCursor{}, fmt.Errorf("timeline: invalid input rate %d", inRate)
	}
	if outRate <= 0 || outRate > 1_000_000_000 {
		return SampleCursor{}, fmt.Errorf("timeline: invalid output rate %d", outRate)
	}
	if srcStart < 0 || outStart < 0 || outCount < 0 {
		return SampleCursor{}, fmt.Errorf("timeline: negative cursor origin or count")
	}
	return SampleCursor{InRate: inRate, OutRate: outRate, SrcStart: srcStart, OutStart: outStart, count: outCount}, nil
}

// Count returns the number of output samples in this run.
func (c SampleCursor) Count() int64 { return c.count }

// CursorCount returns how many output samples cover n input samples under the
// kernel's single rounding rule, without constructing a cursor.
func CursorCount(n, inRate, outRate int64) (int64, error) {
	if n < 0 {
		return 0, fmt.Errorf("timeline: negative input length %d", n)
	}
	if inRate <= 0 || outRate <= 0 {
		return 0, fmt.Errorf("timeline: invalid rates %d -> %d", inRate, outRate)
	}
	r, ok := mulDivRound(n, outRate, inRate)
	if !ok {
		return 0, fmt.Errorf("timeline: cursor count overflow %d", n)
	}
	return r, nil
}

// Phase is the fractional read position of one output sample. Remainder is in
// [0, OutRate) and the earlier source neighbour is Base; linear interpolation
// weights are Remainder/OutRate and 1-Remainder/OutRate.
type Phase struct {
	Base      int64
	Remainder int64
}

// At returns the phase of output sample n within the cursor's run.
func (c SampleCursor) At(n int64) (Phase, error) {
	if c.InRate <= 0 || c.OutRate <= 0 {
		return Phase{}, fmt.Errorf("timeline: uninitialized cursor")
	}
	if n < 0 || n >= c.count {
		return Phase{}, fmt.Errorf("timeline: cursor index %d outside [0,%d)", n, c.count)
	}
	hi, lo := bits.Mul64(uint64(n), uint64(c.InRate))
	if hi >= uint64(c.OutRate) {
		return Phase{}, fmt.Errorf("timeline: cursor position overflow at %d", n)
	}
	base, rem := bits.Div64(hi, lo, uint64(c.OutRate))
	if base > 1<<62 {
		return Phase{}, fmt.Errorf("timeline: cursor base overflow at %d", n)
	}
	return Phase{Base: int64(base), Remainder: int64(rem)}, nil
}

// SourcePosition returns the absolute input sample index the output sample n
// is centred on, rounding halves up. It is used for quality accounting, not
// for filtering.
func (c SampleCursor) SourcePosition(n int64) (int64, error) {
	p, err := c.At(n)
	if err != nil {
		return 0, err
	}
	idx := c.SrcStart + p.Base
	if p.Remainder*2 >= c.OutRate {
		idx++
	}
	return idx, nil
}
