// Package clock implements exact rational time bases and frame timecode
// conversion.
//
// All instants are int64 nanoseconds. Frame rates are exact rationals, and
// every conversion uses integer arithmetic only: there is no floating point
// accumulation anywhere, so repeated conversions of the same input always
// produce the same result on every platform.
package clock

import (
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"

	"media.rebuild/internal/model"
)

// Rate is a frame rate expressed as an exact fraction Num/Den frames per
// second, kept in lowest terms with a positive denominator.
type Rate struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// Well-known rates. 29.97 is represented exactly as 30000/1001.
var (
	Rate24    = Rate{Num: 24, Den: 1}
	Rate25    = Rate{Num: 25, Den: 1}
	Rate2997  = Rate{Num: 30000, Den: 1001}
	Rate23976 = Rate{Num: 24000, Den: 1001}
	Rate50    = Rate{Num: 50, Den: 1}
)

// String renders the rate as "num/den".
func (r Rate) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

// Valid reports whether the rate is usable: both parts positive, denominator
// non-zero and frame index arithmetic stays in range.
func (r Rate) Valid() bool {
	if r.Num <= 0 || r.Den <= 0 {
		return false
	}
	// Rates outside 1..1000 fps are not meaningful for this kernel: below
	// 1 fps a frame spans more than a second, above 1000 fps the nanosecond
	// frame delta collapses to zero.
	return r.Num >= r.Den && r.Num <= 1000*r.Den
}

// Nominal returns the integer count of frame numbers per second used by
// timecode labels (e.g. 30 for 29.97).
func (r Rate) Nominal() int64 {
	return (r.Num + r.Den - 1) / r.Den
}

// SupportsDropFrame reports whether drop-frame timecode is defined for the
// rate. Drop-frame counting exists only for 30000/1001.
func (r Rate) SupportsDropFrame() bool { return r == Rate2997 }

// FrameDurationNs returns the rounded frame duration in nanoseconds. It is
// informational; frame starts are always computed from the frame index.
func (r Rate) FrameDurationNs() int64 {
	q, rem, err := mulDiv(1_000_000_000, uint64(r.Den), uint64(r.Num))
	if err != nil {
		return 0
	}
	if 2*rem >= uint64(r.Num) {
		q++
	}
	return int64(q)
}

// ParseRate accepts the canonical rates used by the review pipeline
// ("24", "25", "29.97", "50", "23.976") and the rational form "num/den".
func ParseRate(s string) (Rate, error) {
	switch strings.TrimSpace(s) {
	case "24":
		return Rate24, nil
	case "25":
		return Rate25, nil
	case "29.97", "30000/1001":
		return Rate2997, nil
	case "23.976", "24000/1001":
		return Rate23976, nil
	case "50":
		return Rate50, nil
	}
	src := model.Source{Name: "rate", Offset: -1}
	parts := strings.Split(strings.TrimSpace(s), "/")
	if len(parts) != 2 {
		return Rate{}, model.Newf(model.CodeTimecodeRate, src, "rate %q is not supported", s)
	}
	num, err1 := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	den, err2 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err1 != nil || err2 != nil {
		return Rate{}, model.Newf(model.CodeTimecodeRate, src, "rate %q is not a rational number", s)
	}
	r := reduce(num, den)
	if !r.Valid() {
		return Rate{}, model.Newf(model.CodeTimecodeRate, src, "rate %q out of range", s)
	}
	return r, nil
}

func gcd(a, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func reduce(num, den int64) Rate {
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 || den == 0 {
		return Rate{Num: num, Den: den}
	}
	g := gcd(num, den)
	return Rate{Num: num / g, Den: den / g}
}

// mulDiv computes floor(a*b/c) and its remainder for non-negative operands
// using 128-bit intermediate arithmetic. It returns an overflow error when the
// quotient does not fit in 64 bits.
func mulDiv(a, b uint64, c uint64) (uint64, uint64, error) {
	if c == 0 {
		return 0, 0, fmt.Errorf("division by zero")
	}
	hi, lo := bits.Mul64(a, b)
	if hi >= c {
		return 0, 0, fmt.Errorf("multiplication overflow")
	}
	q, r := bits.Div64(hi, lo, c)
	return q, r, nil
}

// frameStartNs converts a non-negative frame index to its start instant in
// nanoseconds, rounding the exact rational value up. Ceiling (rather than
// truncation or round-half-even) is what keeps FrameAt(frameStartNs(f)) == f
// exact at every rate: the instant is never earlier than the true frame
// boundary, and it stays strictly before the next frame's boundary.
func frameStartNs(r Rate, frame int64) (int64, error) {
	if frame < 0 {
		return 0, model.Newf(model.CodeTimecodeFrame, model.Source{Offset: -1}, "negative frame index %d", frame)
	}
	if !r.Valid() {
		return 0, model.Newf(model.CodeTimecodeRate, model.Source{Offset: -1}, "invalid rate %s", r)
	}
	// Exact value is frame*Den*1e9/Num seconds in nanoseconds. The denominator
	// is split across a whole-number part and a remainder so no intermediate
	// multiplication can overflow.
	whole := uint64(1_000_000_000) / uint64(r.Num)
	rest := uint64(1_000_000_000) % uint64(r.Num)
	q, rem, err := mulDiv(uint64(frame), uint64(r.Den)*whole, 1)
	if err != nil {
		return 0, model.New(model.CodeOverflow, model.Source{Offset: -1}, "frame timestamp overflow", err)
	}
	q2, rem2, err := mulDiv(uint64(frame), uint64(r.Den)*rest, uint64(r.Num))
	if err != nil {
		return 0, model.New(model.CodeOverflow, model.Source{Offset: -1}, "frame timestamp overflow", err)
	}
	// Carry the sub-nanosecond remainder from the split, then round up to the
	// next whole nanosecond when the exact value was not integral.
	q += q2
	if frac := (rem + rem2) % uint64(r.Num); frac != 0 {
		q++
	}
	if q > math.MaxInt64 {
		return 0, model.New(model.CodeOverflow, model.Source{Offset: -1}, "frame timestamp overflow", nil)
	}
	return int64(q), nil
}

// FrameAt returns the greatest frame index whose start is <= ns.
func FrameAt(r Rate, ns int64) (int64, error) {
	if ns < 0 {
		return 0, model.Newf(model.CodeTimecodeFrame, model.Source{Offset: -1}, "negative timestamp %d", ns)
	}
	if !r.Valid() {
		return 0, model.Newf(model.CodeTimecodeRate, model.Source{Offset: -1}, "invalid rate %s", r)
	}
	// floor(ns*Num / (1e9*Den)) with a 128-bit intermediate product.
	den := uint64(1_000_000_000) * uint64(r.Den)
	q2, _, err := mulDiv(uint64(ns), uint64(r.Num), den)
	if err != nil {
		return 0, model.New(model.CodeOverflow, model.Source{Offset: -1}, "frame index overflow", err)
	}
	if q2 > math.MaxInt64 {
		return 0, model.New(model.CodeOverflow, model.Source{Offset: -1}, "frame index overflow", nil)
	}
	return int64(q2), nil
}

// CheckedAdd adds two instant values with overflow detection.
func CheckedAdd(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, model.New(model.CodeOverflow, model.Source{Offset: -1}, "time addition overflow", nil)
	}
	return a + b, nil
}

// ValidateSpan checks a [start,end) interval for negative or inverted values.
func ValidateSpan(start, end int64) error {
	if start < 0 {
		return model.Newf(model.CodeInvalidArgument, model.Source{Offset: -1}, "negative start time %d", start)
	}
	if end < start {
		return model.Newf(model.CodeInvalidArgument, model.Source{Offset: -1}, "end %d precedes start %d", end, start)
	}
	return nil
}

// Timecode is a parsed SMPTE-style label. DropFrame is meaningful only for
// 29.97; Frame counts elapsed frame numbers, not wall-clock frames.
type Timecode struct {
	Hours     int64
	Minutes   int64
	Seconds   int64
	Frames    int64
	DropFrame bool
	Rate      Rate
}

// dropFramesPerMinute is the number of frame labels skipped at each minute
// boundary (except every tenth) in 29.97 drop-frame timecode.
const dropFramesPerMinute = 2

// ParseTimecode parses "HH:MM:SS:FF" (non-drop) or "HH:MM:SS;FF" (drop-frame).
// It rejects malformed fields, frame numbers beyond the rate, drop-frame
// labels that would be skipped, and drop-frame syntax on rates other than
// 30000/1001.
func ParseTimecode(s string, rate Rate) (Timecode, error) {
	return ParseTimecodeAt(s, rate, model.Source{Name: "timecode", Offset: -1})
}

// ParseTimecodeAt is ParseTimecode with the error source supplied by the
// caller, so a timecode read from a file reports that file and line.
func ParseTimecodeAt(s string, rate Rate, src model.Source) (Timecode, error) {
	if src.Offset == 0 && src.Line == 0 {
		src.Offset = -1
	}
	tc := Timecode{Rate: rate}
	if !rate.Valid() {
		return tc, model.Newf(model.CodeTimecodeRate, src, "invalid rate %s", rate)
	}
	body := strings.TrimSpace(s)
	sep := ":"
	if strings.Contains(body, ";") {
		sep = ";"
		parts := strings.Split(body, ";")
		if len(parts) != 2 {
			return tc, model.Newf(model.CodeTimecodeSyntax, src, "malformed timecode %q", s)
		}
		framePart := parts[1]
		head := parts[0]
		hp := strings.Split(head, ":")
		if len(hp) != 3 {
			return tc, model.Newf(model.CodeTimecodeSyntax, src, "malformed timecode %q", s)
		}
		fields := []string{hp[0], hp[1], hp[2], framePart}
		return parseFields(fields, true, rate, s, src)
	}
	fields := strings.Split(body, sep)
	if len(fields) != 4 {
		return tc, model.Newf(model.CodeTimecodeSyntax, src, "malformed timecode %q", s)
	}
	return parseFields(fields, false, rate, s, src)
}

func parseFields(fields []string, drop bool, rate Rate, raw string, src model.Source) (Timecode, error) {
	tc := Timecode{DropFrame: drop, Rate: rate}
	vals := make([]int64, 4)
	for i, f := range fields {
		if len(f) != 2 {
			return tc, model.Newf(model.CodeTimecodeSyntax, src, "field %d of %q must have two digits", i+1, raw)
		}
		v, err := strconv.ParseInt(f, 10, 64)
		if err != nil || v < 0 {
			return tc, model.Newf(model.CodeTimecodeSyntax, src, "field %d of %q is not numeric", i+1, raw)
		}
		vals[i] = v
	}
	tc.Hours, tc.Minutes, tc.Seconds, tc.Frames = vals[0], vals[1], vals[2], vals[3]
	if drop && !rate.SupportsDropFrame() {
		return tc, model.Newf(model.CodeTimecodeDropFrame, src, "drop-frame syntax requires 30000/1001, got %s", rate)
	}
	if !drop && strings.Contains(raw, ";") {
		return tc, model.Newf(model.CodeTimecodeSyntax, src, "inconsistent separators in %q", raw)
	}
	nominal := rate.Nominal()
	if tc.Minutes > 59 || tc.Seconds > 59 {
		return tc, model.Newf(model.CodeTimecodeFrame, src, "minute/second out of range in %q", raw)
	}
	if tc.Frames >= nominal {
		return tc, model.Newf(model.CodeTimecodeFrame, src, "frame number %d is not below the nominal rate %d in %q", tc.Frames, nominal, raw)
	}
	if drop {
		// The first two frame labels of every minute are skipped, except at
		// each tenth minute. A label that falls in the skip window cannot
		// exist and indicates a corrupt capture.
		if tc.Minutes%10 != 0 && tc.Seconds == 0 && tc.Frames < dropFramesPerMinute {
			return tc, model.Newf(model.CodeTimecodeDropFrame, src, "frame %02d:%02d:%02d;%02d is skipped by drop-frame counting", tc.Hours, tc.Minutes, tc.Seconds, tc.Frames)
		}
	}
	return tc, nil
}

// FrameNumber returns the absolute frame label, applying drop-frame skipped
// labels.
func (tc Timecode) FrameNumber() int64 {
	nominal := tc.Rate.Nominal()
	n := ((tc.Hours*3600+tc.Minutes*60+tc.Seconds)*nominal + tc.Frames)
	if tc.DropFrame {
		totalMinutes := tc.Hours*60 + tc.Minutes
		n -= dropFramesPerMinute * (totalMinutes - totalMinutes/10)
	}
	return n
}

// TimestampNs returns the instant of the first frame carrying this label,
// exact to the nanosecond after rounding.
func (tc Timecode) TimestampNs() (int64, error) {
	return frameStartNs(tc.Rate, tc.FrameNumber())
}

// FormatFrame renders a frame label in "HH:MM:SS:FF" or, for 29.97 with
// drop=true, in "HH:MM:SS;FF" drop-frame form.
func FormatFrame(rate Rate, frame int64, drop bool) (string, error) {
	if frame < 0 {
		return "", model.Newf(model.CodeTimecodeFrame, model.Source{Offset: -1}, "negative frame %d", frame)
	}
	if drop && !rate.SupportsDropFrame() {
		return "", model.Newf(model.CodeTimecodeDropFrame, model.Source{Offset: -1}, "drop-frame requires 30000/1001")
	}
	nominal := rate.Nominal()
	var tc Timecode
	tc.Rate = rate
	tc.DropFrame = drop
	if !drop {
		tc.Frames = frame % nominal
		total := frame / nominal
		tc.Seconds = total % 60
		tc.Minutes = (total / 60) % 60
		tc.Hours = total / 3600
	} else {
		// Undo the labels skipped at minute boundaries, then split.
		tenMin := nominal*600 - dropFramesPerMinute*9
		tenCount := frame / tenMin
		rem := frame % tenMin
		extra := int64(0)
		if rem >= nominal*60 {
			extra = 1 + (rem-nominal*60)/(nominal*60-dropFramesPerMinute)
		}
		minutes := tenCount*10 + extra
		abs := frame + dropFramesPerMinute*(minutes-minutes/10)
		tc.Hours = abs / (nominal * 3600)
		tc.Minutes = (abs / (nominal * 60)) % 60
		tc.Seconds = (abs / nominal) % 60
		tc.Frames = abs % nominal
	}
	return fmt.Sprintf("%02d:%02d:%02d%s%02d", tc.Hours, tc.Minutes, tc.Seconds, sepFor(drop), tc.Frames), nil
}

func sepFor(drop bool) string {
	if drop {
		return ";"
	}
	return ":"
}
