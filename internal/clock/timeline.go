package clock

import (
	"fmt"
	"math"
	"math/bits"
)

// TickRate is the single shared media time base. Every subsystem expresses
// media time as an integer count of these ticks; nothing converts on its own.
const TickRate int64 = 90000

// Tick is a media instant measured on the shared 90 kHz grid.
type Tick int64

// Offset is a byte position in the original input file. It is never rebased
// or shifted: recovery reporting must point at real input bytes.
type Offset int64

// ErrNoTimeBase reports a media header whose timescale cannot be honoured.
type ErrNoTimeBase struct {
	TrackID   uint32
	Timescale uint32
}

func (e *ErrNoTimeBase) Error() string {
	return fmt.Sprintf("track %d has unusable timescale %d", e.TrackID, e.Timescale)
}

// ErrOutOfRange reports an offset or size outside the readable file.
type ErrOutOfRange struct {
	Offset Offset
	Size   int64
	Limit  int64
	What   string
}

func (e *ErrOutOfRange) Error() string {
	return fmt.Sprintf("%s at byte offset %d with length %d exceeds file length %d", e.What, e.Offset, e.Size, e.Limit)
}

// ErrNegative reports a structurally impossible negative quantity.
type ErrNegative struct {
	Field string
	Value int64
}

func (e *ErrNegative) Error() string {
	return fmt.Sprintf("%s is negative (%d)", e.Field, e.Value)
}

// ErrInternal is used when a caller breaks the one-converter rule by asking
// for an inconsistent unit.
type ErrInternal struct {
	Reason string
}

func (e *ErrInternal) Error() string {
	return "clock: " + e.Reason
}

// ErrOverflow reports a tick value that cannot be represented in int64.
type ErrOverflow struct {
	Value uint64
}

func (e *ErrOverflow) Error() string {
	return fmt.Sprintf("tick value %d overflows int64", e.Value)
}

// Checked reports an offset/size pair that must be fully inside a container of
// the given length. This guard exists so no caller allocates from an untrusted
// header length before bounds are proven.
func Checked(off Offset, size int64, limit int64, what string) (Offset, int64, error) {
	if off < 0 {
		return 0, 0, &ErrNegative{Field: what + " offset", Value: int64(off)}
	}
	if size < 0 {
		return 0, 0, &ErrNegative{Field: what + " size", Value: size}
	}
	if off > Offset(limit) {
		return 0, 0, &ErrOutOfRange{Offset: off, Size: size, Limit: limit, What: what}
	}
	if size > limit-int64(off) {
		return 0, 0, &ErrOutOfRange{Offset: off, Size: size, Limit: limit, What: what}
	}
	return off, size, nil
}

// Conversion is the audited result of mapping an input timescale value onto
// the shared grid. Remainder is what was discarded; it is required so error
// can be reported rather than silently absorbed.
type Conversion struct {
	SourceTimescale uint32
	SourceValue     uint64
	Ticks           Tick
	Remainder       uint64 // in source units, always < SourceTimescale
	Truncated       bool
}

// Err returns the sub-tick loss as a float in [0,1). It is descriptive only:
// the integer result is authoritative.
func (c Conversion) Err() float64 {
	if c.SourceTimescale == 0 {
		return 0
	}
	return float64(c.Remainder) / float64(c.SourceTimescale)
}

// Converter maps one media timescale to the shared tick grid. The remainder is
// retained per conversion, and rounding always floors: [ConvertFloor].
type Converter struct {
	timescale uint32
}

// NewConverter validates the track timescale once. A zero timescale cannot be
// divided, so it is rejected instead of guessed.
func NewConverter(timescale uint32) (Converter, error) {
	if timescale == 0 {
		return Converter{}, &ErrNoTimeBase{Timescale: timescale}
	}
	return Converter{timescale: timescale}, nil
}

// NewConverterForTrack is [NewConverter] with the track identity preserved in
// the error.
func NewConverterForTrack(trackID, timescale uint32) (Converter, error) {
	if timescale == 0 {
		return Converter{}, &ErrNoTimeBase{TrackID: trackID, Timescale: timescale}
	}
	return Converter{timescale: timescale}, nil
}

// Timescale returns the source grid this converter was built from.
func (c Converter) Timescale() uint32 { return c.timescale }

// ConvertFloor maps value source ticks to shared 90 kHz ticks, flooring any
// fractional remainder and recording it in the result.
func (c Converter) ConvertFloor(value uint64) (Conversion, error) {
	if c.timescale == 0 {
		return Conversion{}, &ErrNoTimeBase{Timescale: 0}
	}
	if value > math.MaxUint64/uint64(TickRate) {
		return Conversion{}, &ErrOverflow{Value: value}
	}
	scaled := value * uint64(TickRate)
	quotient := scaled / uint64(c.timescale)
	remainder := scaled % uint64(c.timescale)
	if quotient > math.MaxInt64 {
		return Conversion{}, &ErrOverflow{Value: quotient}
	}
	return Conversion{
		SourceTimescale: c.timescale,
		SourceValue:     value,
		Ticks:           Tick(quotient),
		Remainder:       remainder,
		Truncated:       remainder != 0,
	}, nil
}

// ConvertExact maps value source ticks to the shared grid and refuses
// fractional results. Use it when a caller asserts a value is exactly
// representable; the failure names the lost remainder.
func (c Converter) ConvertExact(value uint64) (Tick, error) {
	conv, err := c.ConvertFloor(value)
	if err != nil {
		return 0, err
	}
	if conv.Truncated {
		return 0, &ErrInexact{Conversion: conv}
	}
	return conv.Ticks, nil
}

// ErrInexact reports a value that does not land on the shared grid.
type ErrInexact struct {
	Conversion Conversion
}

func (e *ErrInexact) Error() string {
	return fmt.Sprintf("%d/%d does not map exactly onto 90 kHz (remainder %d)",
		e.Conversion.SourceValue, e.Conversion.SourceTimescale, e.Conversion.Remainder)
}

// ScaleDuration maps a count of source ticks to shared ticks, flooring the
// fractional result. It shares [Converter.ConvertFloor] so there is still only
// one conversion site.
func (c Converter) ScaleDuration(value uint64) (Tick, Conversion, error) {
	conv, err := c.ConvertFloor(value)
	return conv.Ticks, conv, err
}

// Adder accumulates conversions while keeping the sub-tick remainder in the
// integer domain so drift does not compound across thousands of samples. The
// state is (ticks, carry) where carry is measured in source units and is
// always < timescale.
type Adder struct {
	conv  Converter
	ticks int64
	carry uint64
}

// NewAdder creates an exact 90 kHz accumulator for the given timescale.
func NewAdder(timescale uint32) (*Adder, error) {
	conv, err := NewConverter(timescale)
	if err != nil {
		return nil, err
	}
	return &Adder{conv: conv}, nil
}

// Add advances by value source ticks and returns the exact shared tick count
// reached. The fractional part carries forward across calls.
func (a *Adder) Add(value uint64) (Tick, Conversion, error) {
	if value > uint64(math.MaxInt64) {
		return 0, Conversion{}, &ErrOverflow{Value: value}
	}
	scale := uint64(a.conv.timescale)
	deltaHi, delta := bits.Mul64(value, uint64(TickRate))
	if deltaHi != 0 {
		return 0, Conversion{}, &ErrOverflow{Value: value}
	}
	baseHi, base := bits.Mul64(uint64(a.ticks), scale)
	if baseHi != 0 {
		return 0, Conversion{}, &ErrOverflow{Value: uint64(a.ticks)}
	}
	units, sumCarry := bits.Add64(base, a.carry, 0)
	if sumCarry != 0 {
		return 0, Conversion{}, &ErrOverflow{Value: uint64(a.ticks)}
	}
	units, sumCarry = bits.Add64(units, delta, 0)
	if sumCarry != 0 {
		return 0, Conversion{}, &ErrOverflow{Value: value}
	}
	ticks := units / scale
	if ticks > math.MaxInt64 {
		return 0, Conversion{}, &ErrOverflow{Value: ticks}
	}
	a.ticks = int64(ticks)
	a.carry = units % scale
	return Tick(ticks), Conversion{
		SourceTimescale: a.conv.timescale,
		SourceValue:     value,
		Ticks:           Tick(ticks),
		Remainder:       a.carry,
		Truncated:       a.carry != 0,
	}, nil
}

// Current reports the accumulated time without advancing.
func (a *Adder) Current() Tick { return Tick(a.ticks) }

// Remainder reports the outstanding carry in source units.
func (a *Adder) Remainder() uint64 { return a.carry }
