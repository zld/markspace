// Package clock owns the single shared time model used by the container and
// packet subsystems. All time-base conversion is done with exact integer
// rational arithmetic (math/big); float64 is only produced at the reporting
// boundary. No fixed frame rate is ever assumed: variable frame rates are
// handled by honoring each sample's own declared duration.
package clock

import (
	"math/big"

	"mediasync/internal/fault"
)

// Rat helpers kept as package-level values to avoid reallocating constants.
var (
	big1000  = big.NewRat(1000, 1)
	big1e6   = big.NewRat(1_000_000, 1)
	wrapMod  = int64(1) << 32
	halfWrap = int64(1) << 31
)

// Timebase describes a track clock: ticks per second. A zero timescale is
// illegal per ISO/IEC 14496-12 and rejected here.
type Timebase struct {
	timescale uint32
}

// NewTimebase validates and wraps a timescale value.
func NewTimebase(timescale uint32) (*Timebase, error) {
	if timescale == 0 {
		return nil, fault.New(fault.ErrIllegalTimebase, "", -1,
			"timescale must be non-zero")
	}
	return &Timebase{timescale: timescale}, nil
}

// Timescale returns the ticks-per-second value.
func (t *Timebase) Timescale() uint32 { return t.timescale }

// TicksToSeconds converts an exact tick count to seconds.
func (t *Timebase) TicksToSeconds(ticks int64) *big.Rat {
	return big.NewRat(ticks, int64(t.timescale))
}

// TicksToMillis converts an exact tick count to milliseconds.
func (t *Timebase) TicksToMillis(ticks int64) *big.Rat {
	r := t.TicksToSeconds(ticks)
	return new(big.Rat).Mul(r, big1000)
}

// Millis renders an exact second-valued rational as milliseconds (float64).
func Millis(seconds *big.Rat) float64 {
	ms, _ := new(big.Rat).Mul(seconds, big1000).Float64()
	return ms
}

// AbsDeltaMillis returns |a-b| in milliseconds.
func AbsDeltaMillis(a, b *big.Rat) float64 {
	d := new(big.Rat).Sub(a, b)
	if d.Sign() < 0 {
		d.Neg(d)
	}
	return Millis(d)
}

// RawSample is one variable-duration sample as declared by the container.
// Offset is the sample data's original byte offset, retained for attribution.
type RawSample struct {
	DurationTicks uint32
	Offset        int64
}

// Mark is one point on a track timeline. Raw values are always retained next
// to calibrated values so drift correction never destroys the source data.
type Mark struct {
	Index         int
	RawTicks      int64    // cumulative DTS in track ticks
	DurationTicks int64    // this sample's own declared duration (VFR)
	RawSeconds    *big.Rat // uncalibrated position on the shared timeline
	CalibSeconds  *big.Rat // calibrated position; equals Raw when no drift
	Offset        int64
}

// BuildMarks expands time-to-sample entries into per-sample marks using each
// sample's real duration. It never divides wall time by a fixed frame rate.
func BuildMarks(tb *Timebase, samples []RawSample) ([]Mark, error) {
	marks := make([]Mark, 0, len(samples))
	var dts int64
	for i, s := range samples {
		// Overflow check for the cumulative DTS.
		if dts > dts+int64(s.DurationTicks) {
			return nil, fault.New(fault.ErrOverflow, "", s.Offset,
				"cumulative sample timestamp overflow at sample %d", i)
		}
		raw := tb.TicksToSeconds(dts)
		marks = append(marks, Mark{
			Index:         i,
			RawTicks:      dts,
			DurationTicks: int64(s.DurationTicks),
			RawSeconds:    raw,
			CalibSeconds:  new(big.Rat).Set(raw),
			Offset:        s.Offset,
		})
		dts += int64(s.DurationTicks)
	}
	return marks, nil
}

// Anchor pairs an observed (drifting) track position with the reference
// timeline position the same event should have.
type Anchor struct {
	ObservedTicks int64
	RefSeconds    *big.Rat
}

// DriftReport is the exact linear calibration refSeconds = slope*obsSeconds +
// intercept, plus the drift magnitude in PPM. Both original and corrected
// values stay available to callers.
type DriftReport struct {
	PPM       int64 // rounded drift magnitude, signed
	slope     *big.Rat
	intercept *big.Rat
}

// DetectDrift fits the anchor pairs exactly (two anchors define the line;
// more anchors are checked against it) and decides whether the drift is
// within the configured calibration threshold.
func DetectDrift(tb *Timebase, anchors []Anchor, thresholdPPM int64) (*DriftReport, error) {
	if len(anchors) < 2 {
		return nil, fault.New(fault.ErrDriftTooLarge, "", -1,
			"need at least two anchors to measure drift")
	}
	first, last := anchors[0], anchors[len(anchors)-1]
	obs0 := tb.TicksToSeconds(first.ObservedTicks)
	obs1 := tb.TicksToSeconds(last.ObservedTicks)
	spanObs := new(big.Rat).Sub(obs1, obs0)
	if spanObs.Sign() == 0 {
		return nil, fault.New(fault.ErrIllegalTimebase, "", -1,
			"zero-duration drift window")
	}
	spanRef := new(big.Rat).Sub(last.RefSeconds, first.RefSeconds)
	slope := new(big.Rat).Quo(spanRef, spanObs)
	intercept := new(big.Rat).Sub(first.RefSeconds, new(big.Rat).Mul(slope, obs0))

	// ppm = (1/slope - 1) * 1e6, truncated to an integer.
	ppmRat := new(big.Rat).Sub(new(big.Rat).Quo(big.NewRat(1, 1), slope), big.NewRat(1, 1))
	ppmRat.Mul(ppmRat, big1e6)
	ppm, _ := ppmRat.Float64()
	ppmSigned := int64(ppm)

	// Verify every intermediate anchor lies on the fitted line.
	for _, a := range anchors[1 : len(anchors)-1] {
		pred := new(big.Rat).Add(
			new(big.Rat).Mul(slope, tb.TicksToSeconds(a.ObservedTicks)), intercept)
		if pred.Cmp(a.RefSeconds) != 0 {
			return nil, fault.New(fault.ErrDriftTooLarge, "", -1,
				"non-linear drift: anchor at tick %d deviates", a.ObservedTicks)
		}
	}

	abs := ppmSigned
	if abs < 0 {
		abs = -abs
	}
	if abs > thresholdPPM {
		return nil, fault.New(fault.ErrDriftTooLarge, "", -1,
			"drift %d ppm exceeds threshold %d ppm", ppmSigned, thresholdPPM)
	}
	return &DriftReport{PPM: ppmSigned, slope: slope, intercept: intercept}, nil
}

// Calibrate maps an observed tick count to calibrated reference seconds.
func (d *DriftReport) Calibrate(tb *Timebase, observedTicks int64) *big.Rat {
	return new(big.Rat).Add(
		new(big.Rat).Mul(d.slope, tb.TicksToSeconds(observedTicks)),
		d.intercept)
}

// Apply writes calibrated positions into marks, retaining the raw values.
func (d *DriftReport) Apply(tb *Timebase, marks []Mark) {
	for i := range marks {
		marks[i].CalibSeconds = d.Calibrate(tb, marks[i].RawTicks)
	}
}

// ExtendRTPTimestamps unwraps 32-bit RTP timestamps into a monotonic 64-bit
// series. Input must already be ordered by (extended) sequence number. A jump
// of exactly 2^31 is inherently ambiguous and is reported with the offending
// packet's stream offset; no guessed value is emitted.
func ExtendRTPTimestamps(track string, raw []uint32, offsets []int64) ([]int64, error) {
	if len(raw) != len(offsets) {
		return nil, fault.New(fault.ErrPacketMalformed, track, -1,
			"timestamp/offset table length mismatch")
	}
	out := make([]int64, len(raw))
	if len(raw) == 0 {
		return out, nil
	}
	out[0] = int64(raw[0])
	for i := 1; i < len(raw); i++ {
		// Signed 32-bit modular difference.
		delta := int64(int32(raw[i] - raw[i-1]))
		if delta == halfWrap || delta == -halfWrap {
			return nil, fault.New(fault.ErrWrapAmbiguous, track, offsets[i],
				"timestamp jump of 2^31 at packet %d cannot be unwrapped uniquely", i)
		}
		next := out[i-1] + delta
		if (delta > 0 && next < out[i-1]) || (delta < 0 && next > out[i-1]) {
			return nil, fault.New(fault.ErrOverflow, track, offsets[i],
				"extended timestamp overflow at packet %d", i)
		}
		out[i] = next
	}
	return out, nil
}

// MaxAbsDiffMillis returns the largest |a-b| across paired event times.
func MaxAbsDiffMillis(a, b []*big.Rat) (float64, int) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	worst, worstIdx := 0.0, -1
	for i := 0; i < n; i++ {
		if d := AbsDeltaMillis(a[i], b[i]); d > worst {
			worst, worstIdx = d, i
		}
	}
	return worst, worstIdx
}

// IsHalfWrap reports whether the modular distance between two raw timestamps
// is exactly the ambiguous half-cycle boundary.
func IsHalfWrap(prev, cur uint32) bool {
	d := cur - prev
	return int64(d) == halfWrap || int64(d) == wrapMod-halfWrap
}
