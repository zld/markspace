package clock

import (
	"fmt"
	"strconv"
	"strings"

	"mediamerge/internal/fault"
)

// Rate is a frame rate expressed as a rational frames-per-second value.
type Rate struct {
	Num int64 // frames numerator
	Den int64 // seconds denominator
}

// The frame rates explicitly supported by the specification.
var (
	Rate24     = Rate{24, 1}       // 24/1
	Rate25     = Rate{25, 1}       // 25/1
	Rate2997DF = Rate{30000, 1001} // 30000/1001, drop-frame capable
	Rate2997   = Rate{30000, 1001} // same rate, non-drop usage
)

// Valid reports whether r is a positive rational.
func (r Rate) Valid() bool { return r.Num > 0 && r.Den > 0 }

// NominalFPS is the integer frame count used in SMPTE timecode labels.
func (r Rate) NominalFPS() (int64, error) {
	if !r.Valid() {
		return 0, fault.New(fault.StageClock, fault.KindTimecode, "", "invalid frame rate")
	}
	// Nominal SMPTE frame count rounds the rational rate (30000/1001 -> 30).
	return (r.Num + r.Den/2) / r.Den, nil
}

// DropCapable reports whether the rate may carry a drop-frame label.
// Only 30000/1001 is a drop-frame rate among the listed timebases.
func (r Rate) DropCapable() bool { return r.Num == 30000 && r.Den == 1001 }

// FromTimescale maps a timestamp in an arbitrary media timescale to ticks.
func FromTimescale(ts int64, timescale uint32) (Ticks, error) {
	if timescale == 0 {
		return 0, fault.New(fault.StageClock, fault.KindTimecode, "", "zero media timescale")
	}
	return Ticks(ts).Scale(Resolution, int64(timescale))
}

// FramesToTicks converts a zero-based frame number under rate r to ticks.
func FramesToTicks(frames int64, r Rate) (Ticks, error) {
	if !r.Valid() {
		return 0, fault.New(fault.StageClock, fault.KindTimecode, "", "invalid frame rate")
	}
	if frames < 0 {
		return 0, fault.New(fault.StageClock, fault.KindOverflow, "", "negative frame number")
	}
	num, err := mul64(frames, Resolution)
	if err != nil {
		return 0, err
	}
	if num, err = mul64(num, r.Den); err != nil {
		return 0, err
	}
	return Ticks(num / r.Num), nil
}

// TicksToFrames is the inverse of FramesToTicks (floor division).
func TicksToFrames(t Ticks, r Rate) (int64, error) {
	if !r.Valid() {
		return 0, fault.New(fault.StageClock, fault.KindTimecode, "", "invalid frame rate")
	}
	num, err := mul64(int64(t), r.Num)
	if err != nil {
		return 0, err
	}
	den, err := mul64(Resolution, r.Den)
	if err != nil {
		return 0, err
	}
	return num / den, nil
}

// ParseTimecode parses an HH:MM:SS:FF (NDF) or HH;MM;SS;FF (DF) label under
// rate r and returns the zero-based frame number and whether drop-frame
// punctuation was present.
func ParseTimecode(s string, r Rate) (frames int64, df bool, err error) {
	if !r.Valid() {
		return 0, false, fault.New(fault.StageClock, fault.KindTimecode, "", "invalid frame rate")
	}
	parts, df, err := splitTC(s)
	if err != nil {
		return 0, false, err
	}
	if df && !r.DropCapable() {
		return 0, true, fault.New(fault.StageClock, fault.KindDropFrame, "",
			fmt.Sprintf("drop-frame label %q is illegal for rate %d/%d", s, r.Num, r.Den))
	}
	hh, mm, ss, ff, err := parseTCFields(parts)
	if err != nil {
		return 0, df, err
	}
	if hh >= 24 {
		return 0, df, fault.New(fault.StageClock, fault.KindOverflow, "",
			fmt.Sprintf("timecode hour %d overflows the 24h day", hh))
	}
	fps, err := r.NominalFPS()
	if err != nil {
		return 0, df, err
	}
	if ff >= fps {
		return 0, df, fault.New(fault.StageClock, fault.KindTimecode, "",
			fmt.Sprintf("frame number %d exceeds rate nominal fps %d", ff, fps))
	}
	mins, err := mul64(hh, 60)
	if err != nil {
		return 0, df, err
	}
	if mins, err = add64(mins, mm); err != nil {
		return 0, df, err
	}
	secs, err := mul64(mins, 60)
	if err != nil {
		return 0, df, err
	}
	if secs, err = add64(secs, ss); err != nil {
		return 0, df, err
	}
	total, err := mul64(secs, fps)
	if err != nil {
		return 0, df, err
	}
	if total, err = add64(total, ff); err != nil {
		return 0, df, err
	}
	if df {
		// In DF labels the nominal frame count runs ahead of the true frame
		// number by the skipped frames: 2 per minute, 0 at every tenth minute.
		dropped, err := mul64(mins, 2)
		if err != nil {
			return 0, df, err
		}
		skip10, err := mul64(mins/10, 2)
		if err != nil {
			return 0, df, err
		}
		if total, err = add64(total, -(dropped - skip10)); err != nil {
			return 0, df, err
		}
		if mm%10 != 0 && ss == 0 && ff < 2 {
			return 0, df, fault.New(fault.StageClock, fault.KindTimecode, "",
				fmt.Sprintf("drop-frame timecode %q names a skipped frame", s))
		}
	}
	return total, df, nil
}

func splitTC(s string) ([]string, bool, error) {
	seps := []byte{}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ':' || c == ';' || c == '.' {
			seps = append(seps, c)
		}
	}
	if len(seps) != 3 {
		return nil, false, fault.New(fault.StageClock, fault.KindTimecode, "",
			fmt.Sprintf("timecode %q must have the form HH:MM:SS:FF", s))
	}
	df := false
	for _, c := range seps {
		if c == ';' {
			df = true
		}
		if c == '.' {
			return nil, false, fault.New(fault.StageClock, fault.KindTimecode, "",
				fmt.Sprintf("timecode %q uses a decimal separator where a frame field is required", s))
		}
	}
	return strings.FieldsFunc(s, func(c rune) bool { return c == ':' || c == ';' }), df, nil
}

func parseTCFields(parts []string) (hh, mm, ss, ff int64, err error) {
	if len(parts) != 4 {
		return 0, 0, 0, 0, fault.New(fault.StageClock, fault.KindTimecode, "", "timecode needs four fields")
	}
	vals := make([]int64, 4)
	for i, p := range parts {
		v, perr := strconv.ParseInt(p, 10, 64)
		if perr != nil || v < 0 {
			return 0, 0, 0, 0, fault.New(fault.StageClock, fault.KindTimecode, "",
				"timecode field "+p+" is not a non-negative integer")
		}
		vals[i] = v
	}
	hh, mm, ss, ff = vals[0], vals[1], vals[2], vals[3]
	if mm >= 60 || ss >= 60 {
		return 0, 0, 0, 0, fault.New(fault.StageClock, fault.KindTimecode, "",
			"timecode minute/second fields must be below 60")
	}
	return hh, mm, ss, ff, nil
}
