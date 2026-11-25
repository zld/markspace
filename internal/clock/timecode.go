// Package clock provides the shared nanosecond time base and SMPTE timecode
// conversions used by subtitles, media probes, RTP samples, and policy.
package clock

import (
	"math/big"
	"regexp"
	"strconv"

	"archivecheck/internal/diagnostic"
)

// Nanoseconds is the common integer time basis for every subsystem.
type Nanoseconds int64

const (
	Nanosecond           Nanoseconds = 1
	Microsecond                      = 1000 * Nanosecond
	Millisecond                      = 1_000_000 * Nanosecond
	Second                           = 1_000 * Millisecond
	Minute                           = 60 * Second
	Hour                             = 60 * Minute
	nanosecondsPerSecond uint64      = 1_000_000_000
)

// Rate is an exact frame rate. DropFrame is meaningful for 30000/1001.
type Rate struct {
	Name        string
	Numerator   uint64
	Denominator uint64
	NominalFPS  int
	DropFrame   bool
}

var (
	Rate24    = Rate{Name: "24", Numerator: 24, Denominator: 1, NominalFPS: 24}
	Rate25    = Rate{Name: "25", Numerator: 25, Denominator: 1, NominalFPS: 25}
	Rate2997N = Rate{Name: "29.97NDF", Numerator: 30000, Denominator: 1001, NominalFPS: 30}
	Rate2997D = Rate{Name: "29.97DF", Numerator: 30000, Denominator: 1001, NominalFPS: 30, DropFrame: true}
	Rate50    = Rate{Name: "50", Numerator: 50, Denominator: 1, NominalFPS: 50}
)

// ParseRate parses 24, 25, 29.97NDF, 29.97DF, or 50.
func ParseRate(s string) (Rate, error) {
	switch s {
	case "24":
		return Rate24, nil
	case "25":
		return Rate25, nil
	case "29.97", "29.97NDF":
		return Rate2997N, nil
	case "29.97DF":
		return Rate2997D, nil
	case "50":
		return Rate50, nil
	default:
		return Rate{}, diagnostic.New(
			diagnostic.CodeInvalidArgument, diagnostic.PhaseClock, -1, 0,
			"unsupported frame rate %q (want 24, 25, 29.97DF, 29.97NDF, or 50)", s,
		)
	}
}

var timecodePattern = regexp.MustCompile(`^([0-9]+)([:;])([0-9]{2})([:;])([0-9]{2})([:;])([0-9]+)$`)

// ParseTimecode converts HH:MM:SS:FF or HH:MM:SS;FF to nanoseconds.
//
// sample and sourceOffset are attached to every structured failure. The
// sourceOffset is the byte position of the timecode token in its source.
func ParseTimecode(token string, rate Rate, sample int, sourceOffset int64) (Nanoseconds, error) {
	if token == "" {
		return 0, diagnostic.New(diagnostic.CodeInvalidTimecode, diagnostic.PhaseClock, sample, sourceOffset, "empty timecode")
	}
	if token[0] == '-' {
		return 0, diagnostic.New(diagnostic.CodeNegativeTime, diagnostic.PhaseClock, sample, sourceOffset,
			"negative timecode %q is not representable on the archive timeline", token)
	}
	if token[0] == '+' {
		token = token[1:]
		sourceOffset++
	}

	m := timecodePattern.FindStringSubmatchIndex(token)
	if m == nil {
		return 0, diagnostic.New(diagnostic.CodeInvalidTimecode, diagnostic.PhaseClock, sample, sourceOffset,
			"malformed timecode %q", token)
	}

	hour, err := parseField(token, m[2], m[3])
	if err != nil {
		return 0, overflowOrInvalid(err, sample, sourceOffset+int64(m[2]), "hour", token)
	}
	minute, err := parseField(token, m[6], m[7])
	if err != nil {
		return 0, overflowOrInvalid(err, sample, sourceOffset+int64(m[6]), "minute", token)
	}
	second, err := parseField(token, m[10], m[11])
	if err != nil {
		return 0, overflowOrInvalid(err, sample, sourceOffset+int64(m[10]), "second", token)
	}
	frame, err := parseField(token, m[14], m[15])
	if err != nil {
		return 0, overflowOrInvalid(err, sample, sourceOffset+int64(m[14]), "frame", token)
	}

	frameOffset := sourceOffset + int64(m[14])
	if minute >= 60 {
		return 0, diagnostic.New(diagnostic.CodeInvalidTimecode, diagnostic.PhaseClock, sample,
			sourceOffset+int64(m[6]), "minute %d out of range in %q", minute, token)
	}
	if second >= 60 {
		return 0, diagnostic.New(diagnostic.CodeInvalidTimecode, diagnostic.PhaseClock, sample,
			sourceOffset+int64(m[10]), "second %d out of range in %q", second, token)
	}
	if frame >= uint64(rate.NominalFPS) {
		return 0, diagnostic.New(diagnostic.CodeInvalidTimecode, diagnostic.PhaseClock, sample,
			frameOffset, "frame %d out of range for %s in %q", frame, rate.Name, token)
	}

	lastSep := token[m[12]:m[13]]
	if lastSep == ";" && !rate.DropFrame {
		return 0, diagnostic.New(diagnostic.CodeInvalidTimecode, diagnostic.PhaseClock, sample,
			sourceOffset+int64(m[12]), "drop-frame separator used with %s", rate.Name)
	}
	if rate.DropFrame && second == 0 && (frame == 0 || frame == 1) && minute%10 != 0 {
		return 0, diagnostic.New(diagnostic.CodeIllegalDropFrame, diagnostic.PhaseClock, sample,
			frameOffset, "dropped timecode number %q is illegal for %s", token, rate.Name)
	}

	frameIndex, err := smpteFrameIndex(hour, minute, second, frame, rate.NominalFPS, rate.DropFrame)
	if err != nil {
		return 0, diagnostic.Wrap(diagnostic.CodeOverflow, diagnostic.PhaseClock, sample, sourceOffset, err,
			"timecode %q overflows frame counter", token)
	}
	return FramesToNanoseconds(frameIndex, rate, sample, sourceOffset)
}

func parseField(s string, start, end int) (uint64, error) {
	v, err := strconv.ParseUint(s[start:end], 10, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func overflowOrInvalid(err error, sample int, offset int64, field, token string) error {
	if numErr, ok := err.(*strconv.NumError); ok && numErr.Err == strconv.ErrRange {
		return diagnostic.Wrap(diagnostic.CodeOverflow, diagnostic.PhaseClock, sample, offset, err,
			"%s field overflows in %q", field, token)
	}
	return diagnostic.Wrap(diagnostic.CodeInvalidTimecode, diagnostic.PhaseClock, sample, offset, err,
		"invalid %s field in %q", field, token)
}

func smpteFrameIndex(h, m, s, f uint64, nominalFPS int, drop bool) (uint64, error) {
	nominal := uint64(nominalFPS)
	parts := []struct{ value, factor uint64 }{
		{h, 3600 * nominal},
		{m, 60 * nominal},
		{s, nominal},
		{f, 1},
	}
	var total uint64
	for _, p := range parts {
		add, ok := mulOverflow(p.value, p.factor)
		if !ok {
			return 0, strconv.ErrRange
		}
		total, ok = addOverflow(total, add)
		if !ok {
			return 0, strconv.ErrRange
		}
	}
	if drop {
		totalMinutes, ok := mulOverflow(h, 60)
		if !ok {
			return 0, strconv.ErrRange
		}
		totalMinutes, ok = addOverflow(totalMinutes, m)
		if !ok {
			return 0, strconv.ErrRange
		}
		notTens, ok := subOverflow(totalMinutes, totalMinutes/10)
		if !ok {
			return 0, strconv.ErrRange
		}
		dropped, ok := mulOverflow(2, notTens)
		if !ok {
			return 0, strconv.ErrRange
		}
		if total < dropped {
			return 0, strconv.ErrRange
		}
		total -= dropped
	}
	return total, nil
}

// FramesToNanoseconds applies the exact rational frame duration.
func FramesToNanoseconds(frames uint64, rate Rate, sample int, offset int64) (Nanoseconds, error) {
	value := new(big.Int).SetUint64(frames)
	value.Mul(value, new(big.Int).SetUint64(nanosecondsPerSecond))
	value.Mul(value, new(big.Int).SetUint64(rate.Denominator))
	den := new(big.Int).SetUint64(rate.Numerator)

	// Round to nearest nanosecond so adjacent integer rates have stable values.
	half := new(big.Int).Rsh(den, 1)
	value.Add(value, half)
	value.Quo(value, den)
	if !value.IsInt64() {
		return 0, diagnostic.New(diagnostic.CodeOverflow, diagnostic.PhaseClock, sample, offset,
			"%d frames at %s overflows int64 nanoseconds", frames, rate.Name)
	}
	return Nanoseconds(value.Int64()), nil
}

func mulOverflow(a, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > ^uint64(0)/b {
		return 0, false
	}
	return a * b, true
}

func addOverflow(a, b uint64) (uint64, bool) {
	if b > ^uint64(0)-a {
		return 0, false
	}
	return a + b, true
}

func subOverflow(a, b uint64) (uint64, bool) {
	if b > a {
		return 0, false
	}
	return a - b, true
}
