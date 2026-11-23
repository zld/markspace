package clock

import (
	"strconv"
	"strings"

	"mediaverify/internal/errs"
)

// FrameRate describes one supported frame standard. Num/Den is seconds per
// frame (so 29.97fps is 1001/30000 s). Nominal is the integer frame rate
// used by SMPTE timecode arithmetic (30 for 29.97, 60 for 59.94).
type FrameRate struct {
	Num     int64
	Den     int64
	Nominal int64
	Drop    bool
	Name    string
}

// Standard frame rates: 24, 25, 30, 50, 59.94 and 29.97 (DF/NDF).
func FrameRate24() FrameRate    { return FrameRate{1, 24, 24, false, "24"} }
func FrameRate25() FrameRate    { return FrameRate{1, 25, 25, false, "25"} }
func FrameRate30() FrameRate    { return FrameRate{1, 30, 30, false, "30"} }
func FrameRate50() FrameRate    { return FrameRate{1, 50, 50, false, "50"} }
func FrameRate2997(drop bool) FrameRate {
	return FrameRate{1001, 30000, 30, drop, map[bool]string{true: "29.97DF", false: "29.97NDF"}[drop]}
}
func FrameRate5994(drop bool) FrameRate {
	return FrameRate{1001, 60000, 60, drop, map[bool]string{true: "59.94DF", false: "59.94NDF"}[drop]}
}

// Rational returns the seconds-per-frame rational.
func (fr FrameRate) Rational() Rational { return Rational{fr.Num, fr.Den} }

// FrameDurationNS is the exact duration of one frame on the ns scale.
func (fr FrameRate) FrameDurationNS() Ticks { return FrameDuration(fr.Rational()) }

// dropCount is the number of frame numbers skipped at a drop minute.
func (fr FrameRate) dropCount() int64 {
	if !fr.Drop {
		return 0
	}
	return fr.Nominal / 15 // 2 for 29.97, 4 for 59.94
}

// Timecode is a wall-clock SMPTE timecode. Drop mirrors the ';' marker.
type Timecode struct {
	Hour, Min, Sec, Frame int
	Drop                  bool
}

// ParseTimecode accepts HH:MM:SS:FF (NDF) and HH:MM:SS;FF (DF). The rate's
// drop mode must agree with the separator, and every component is range
// checked, including illegal dropped frame numbers.
func ParseTimecode(s string, fr FrameRate) (Timecode, error) {
	drop := strings.Contains(s, ";")
	sep := byte(':')
	if drop {
		sep = ';'
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ':' || r == ';' })
	if len(parts) != 4 {
		return Timecode{}, errs.New(errs.KindInvalidInput, "timecode", "want HH:MM:SS%cFF got %q", sep, s)
	}
	vals := [4]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || len(p) != 2 || n < 0 {
			return Timecode{}, errs.New(errs.KindInvalidInput, "timecode", "bad component %q in %q", p, s)
		}
		vals[i] = n
	}
	tc := Timecode{vals[0], vals[1], vals[2], vals[3], drop}
	if drop != fr.Drop {
		return Timecode{}, errs.New(errs.KindTimestamp, "timecode",
			"drop marker mismatch: code %s vs rate %s", s, fr.Name)
	}
	if tc.Hour > 23 || tc.Min > 59 || tc.Sec > 59 || int64(tc.Frame) >= fr.Nominal {
		return Timecode{}, errs.New(errs.KindInvalidInput, "timecode", "component out of range in %q", s)
	}
	if fr.Drop && tc.Sec == 0 && int64(tc.Frame) < fr.dropCount() && tc.Min%10 != 0 {
		return Timecode{}, errs.New(errs.KindTimestamp, "timecode",
			"illegal dropped frame number %q (%s)", s, fr.Name)
	}
	return tc, nil
}

// String renders the canonical marker form.
func (tc Timecode) String() string {
	last := byte(':')
	if tc.Drop {
		last = ';'
	}
	return strconv.Itoa(tc.Hour) + ":" + two(tc.Min) + ":" + two(tc.Sec) +
		string(last) + two(tc.Frame)
}

func two(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// TotalFrames converts a timecode to a zero-based frame index using SMPTE
// arithmetic (drop-frame compensation where applicable).
func (tc Timecode) TotalFrames(fr FrameRate) (int64, error) {
	if tc.Drop != fr.Drop {
		return 0, errs.New(errs.KindTimestamp, "timecode", "drop mode mismatch vs %s", fr.Name)
	}
	n := fr.Nominal
	totalMinutes := int64(tc.Hour)*60 + int64(tc.Min)
	frames := ((totalMinutes*60 + int64(tc.Sec)) * n) + int64(tc.Frame)
	if fr.Drop {
		frames -= fr.dropCount() * (totalMinutes - totalMinutes/10)
	}
	return frames, nil
}

// FromTotalFrames is the inverse of TotalFrames.
func FromTotalFrames(frames int64, fr FrameRate) (Timecode, error) {
	if frames < 0 {
		return Timecode{}, errs.New(errs.KindTimestamp, "timecode", "negative frame index %d", frames)
	}
	n := fr.Nominal
	if !fr.Drop {
		frame := frames % n
		total := frames / n
		sec := total % 60
		total /= 60
		min := total % 60
		hour := total / 60
		return Timecode{int(hour), int(min), int(sec), int(frame), false}, nil
	}
	drop := fr.dropCount()
	framesPerMin := 60 * n
	framesPer10 := 10*framesPerMin - 9*drop
	d := frames / framesPer10
	r := frames % framesPer10
	if r > drop {
		r = r + drop*((r-drop)/framesPerMin)
	} else {
		r += drop
	}
	hour := d / 6
	d %= 6
	return Timecode{int(hour), int(d), int(r / n), int(r % n), true}, nil
}

// ToTicks places a timecode on the shared nanosecond scale.
func (tc Timecode) ToTicks(fr FrameRate) (Ticks, error) {
	f, err := tc.TotalFrames(fr)
	if err != nil {
		return 0, err
	}
	return ScaleToTicks(f, fr.Rational())
}

// TicksToTimecode snaps arbitrary ticks to the nearest frame index and
// renders it. Snapping error never exceeds half a frame (within the one
// frame round-trip tolerance).
func TicksToTimecode(t Ticks, fr FrameRate) (Timecode, error) {
	f, err := TicksToScale(t, fr.Rational())
	if err != nil {
		return Timecode{}, err
	}
	return FromTotalFrames(f, fr)
}
