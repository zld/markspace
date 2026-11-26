// Package clock provides the single media time base shared by every parser.
// One Tick is 1/90000 s, which represents 24, 25, 29.97 (1001/30000 s) and
// 50 fps frame durations exactly, so no nominal-frame rounding leaks into
// sample-table or subtitle arithmetic.
package clock

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Tick is a media timestamp on the 90 kHz shared time base.
type Tick int64

// TickRate is the number of ticks per second.
const TickRate int64 = 90000

// FromMilliseconds converts a millisecond timestamp exactly.
func FromMilliseconds(ms int64) Tick { return Tick(ms * (TickRate / 1000)) }

// FromSeconds converts a duration with rounding to the nearest tick.
func FromSeconds(sec float64) Tick {
	if sec >= 0 {
		return Tick(sec*float64(TickRate) + 0.5)
	}
	return Tick(sec*float64(TickRate) - 0.5)
}

// FromMicroseconds converts a microsecond timestamp with rounding.
func FromMicroseconds(us int64) Tick {
	// 90000/1e6 = 9/100.
	if us >= 0 {
		return Tick((us*9 + 50) / 100)
	}
	return Tick((us*9 - 50) / 100)
}

// Milliseconds returns the timestamp truncated toward zero in milliseconds.
func (t Tick) Milliseconds() int64 { return int64(t) / (TickRate / 1000) }

// Seconds returns the timestamp as seconds.
func (t Tick) Seconds() float64 { return float64(t) / float64(TickRate) }

// String formats the tick as HH:MM:SS.mmm.
func (t Tick) String() string {
	neg := t < 0
	v := int64(t)
	if neg {
		v = -v
	}
	ms := v / 90
	h := ms / 3600000
	ms %= 3600000
	m := ms / 60000
	ms %= 60000
	s := ms / 1000
	ms %= 1000
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%02d:%02d:%02d.%03d", sign, h, m, s, ms)
}

// Rate is a frame rate as an exact rational number.
type Rate struct {
	Num int64 // frames per Den seconds
	Den int64
}

var (
	Rate24   = Rate{Num: 24, Den: 1}
	Rate25   = Rate{Num: 25, Den: 1}
	Rate2997 = Rate{Num: 30000, Den: 1001}
	Rate50   = Rate{Num: 50, Den: 1}
)

// NominalFPS is the integer rate used for timecode frame numbering.
func (r Rate) NominalFPS() int {
	if r.Num%r.Den == 0 {
		return int(r.Num / r.Den)
	}
	return int((r.Num + r.Den - 1) / r.Den)
}

// FrameTicks is the exact frame duration if it is integral, else the closest
// tick count. The supported rates are exact.
func (r Rate) FrameTicks() Tick {
	n := r.Num
	d := r.Den
	if d <= 0 || n <= 0 {
		return 0
	}
	// ticks = TickRate * Den / Num, rounded half up.
	return Tick((TickRate*d + n/2) / n)
}

// ExactFrameTicks reports whether TickRate*Den is divisible by Num.
func (r Rate) ExactFrameTicks() bool {
	return r.Num > 0 && (TickRate*r.Den)%r.Num == 0
}

func (r Rate) String() string {
	if r.Den == 1 {
		return strconv.FormatInt(r.Num, 10)
	}
	if r.Num == 30000 && r.Den == 1001 {
		return "29.97"
	}
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

// ParseRate accepts the rates required by the delivery contract, plus their
// rational spellings.
func ParseRate(s string) (Rate, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "24", "24fps":
		return Rate24, true
	case "25", "25fps", "pal":
		return Rate25, true
	case "29.97", "29.97df", "29.97ndf", "29.97fps", "30000/1001", "ntsc":
		return Rate2997, true
	case "50", "50fps":
		return Rate50, true
	}
	return Rate{}, false
}

// Timecode is a frame-accurate SMPTE-style timestamp.
type Timecode struct {
	Rate    Rate
	Drop    bool
	Hours   int
	Minutes int
	Seconds int
	Frames  int
}

var (
	ErrTimecodeSyntax   = errors.New("invalid timecode syntax")
	ErrTimecodeRange    = errors.New("timecode field out of range")
	ErrTimecodeDrop     = errors.New("drop-frame timecode not allowed for rate")
	ErrDroppedFrameUsed = errors.New("drop-frame timecode names a dropped frame number")
	ErrTimeSyntax       = errors.New("invalid clock time syntax")
	ErrTimeRange        = errors.New("clock time field out of range")
	ErrNegativeDuration = errors.New("duration is negative")
)

// ParseTimecode parses HH:MM:SS:FF (non-drop) or HH:MM:SS;FF (drop).
// The supplied rate governs frame numbering; DF is legal only at 29.97.
func ParseTimecode(s string, rate Rate) (Timecode, error) {
	if rate.Num <= 0 || rate.Den <= 0 {
		return Timecode{}, fmt.Errorf("%w: invalid rate", ErrTimecodeSyntax)
	}
	s = strings.TrimSpace(s)
	drop := false
	switch {
	case strings.ContainsRune(s, ';'):
		drop = true
		if strings.Count(s, ";") != 1 || strings.LastIndex(s, ";") != len(s)-3 {
			return Timecode{}, fmt.Errorf("%w: %q", ErrTimecodeSyntax, s)
		}
		s = strings.Replace(s, ";", ":", 1)
	case strings.ContainsRune(s, ','):
		drop = true
		if strings.Count(s, ",") != 1 || strings.LastIndex(s, ",") != len(s)-3 {
			return Timecode{}, fmt.Errorf("%w: %q", ErrTimecodeSyntax, s)
		}
		s = strings.Replace(s, ",", ":", 1)
	}
	parts := strings.Split(s, ":")
	if len(parts) != 4 {
		return Timecode{}, fmt.Errorf("%w: %q", ErrTimecodeSyntax, s)
	}
	vals := make([]int, 4)
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || len(p) > 9 {
			return Timecode{}, fmt.Errorf("%w: %q", ErrTimecodeSyntax, s)
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return Timecode{}, fmt.Errorf("%w: %q", ErrTimecodeSyntax, s)
			}
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return Timecode{}, fmt.Errorf("%w: %q", ErrTimecodeSyntax, s)
		}
		vals[i] = v
	}
	tc := Timecode{Rate: rate, Drop: drop, Hours: vals[0], Minutes: vals[1], Seconds: vals[2], Frames: vals[3]}
	if tc.Minutes > 59 || tc.Seconds > 59 {
		return Timecode{}, fmt.Errorf("%w: %02d:%02d", ErrTimecodeRange, tc.Minutes, tc.Seconds)
	}
	nominal := rate.NominalFPS()
	if tc.Frames >= nominal {
		return Timecode{}, fmt.Errorf("%w: frame %d >= %d", ErrTimecodeRange, tc.Frames, nominal)
	}
	if drop {
		if rate != Rate2997 {
			return Timecode{}, fmt.Errorf("%w: %s", ErrTimecodeDrop, rate)
		}
		totalMinutes := tc.Hours*60 + tc.Minutes
		if tc.Seconds == 0 && tc.Minutes%10 != 0 && tc.Frames < 2 && totalMinutes > 0 {
			return Timecode{}, fmt.Errorf("%w: %02d:%02d:%02d;%02d", ErrDroppedFrameUsed,
				tc.Hours, tc.Minutes, tc.Seconds, tc.Frames)
		}
	}
	return tc, nil
}

// TotalFrames returns the frame number encoded by the timecode.
func (tc Timecode) TotalFrames() int64 {
	nominal := int64(tc.Rate.NominalFPS())
	totalMinutes := int64(tc.Hours*60 + tc.Minutes)
	frames := (int64(tc.Hours)*3600+int64(tc.Minutes)*60+int64(tc.Seconds))*nominal + int64(tc.Frames)
	if tc.Drop {
		frames -= 2 * (totalMinutes - totalMinutes/10)
	}
	return frames
}

// Ticks converts the timecode onto the shared 90 kHz base. The result is
// frame-exact for every supported rate (error well under one tick).
func (tc Timecode) Ticks() (Tick, error) {
	if tc.Rate.Num <= 0 || tc.Rate.Den <= 0 {
		return 0, fmt.Errorf("%w: invalid rate", ErrTimecodeSyntax)
	}
	if tc.Minutes > 59 || tc.Seconds > 59 || tc.Frames >= tc.Rate.NominalFPS() {
		return 0, fmt.Errorf("%w: %02d:%02d:%02d:%02d", ErrTimecodeRange,
			tc.Hours, tc.Minutes, tc.Seconds, tc.Frames)
	}
	frames := tc.TotalFrames()
	if frames < 0 {
		return 0, fmt.Errorf("%w: negative frame count", ErrTimecodeRange)
	}
	// ticks = frames * 90000 * Den / Num, rounded half up. Supported rates
	// divide exactly, so this is lossless.
	num := frames*TickRate*tc.Rate.Den + tc.Rate.Num/2
	return Tick(num / tc.Rate.Num), nil
}

// String formats the timecode with its original separator convention.
func (tc Timecode) String() string {
	sep := ":"
	if tc.Drop {
		sep = ";"
	}
	return fmt.Sprintf("%02d:%02d:%02d%s%02d", tc.Hours, tc.Minutes, tc.Seconds, sep, tc.Frames)
}

// ParseSRTTime parses HH:MM:SS,mmm with strict field widths.
func ParseSRTTime(s string) (Tick, error) {
	s = strings.TrimSpace(s)
	if len(s) != 12 || s[2] != ':' || s[5] != ':' || s[8] != ',' {
		return 0, fmt.Errorf("%w: %q", ErrTimeSyntax, s)
	}
	return parseClockFields(s[:2], s[3:5], s[6:8], s[9:12], "SRT")
}

// ParseVTTTime parses HH:MM:SS.mmm or MM:SS.mmm with strict field widths.
func ParseVTTTime(s string) (Tick, error) {
	s = strings.TrimSpace(s)
	var hh, mm, ss, ms string
	switch {
	case len(s) == 12 && s[2] == ':' && s[5] == ':' && s[8] == '.':
		hh, mm, ss, ms = s[:2], s[3:5], s[6:8], s[9:12]
	case len(s) == 9 && s[2] == ':' && s[5] == '.':
		hh, mm, ss, ms = "00", s[:2], s[3:5], s[6:9]
	default:
		return 0, fmt.Errorf("%w: %q", ErrTimeSyntax, s)
	}
	return parseClockFields(hh, mm, ss, ms, "VTT")
}

func parseClockFields(hh, mm, ss, ms, kind string) (Tick, error) {
	for _, f := range []string{hh, mm, ss, ms} {
		for i := 0; i < len(f); i++ {
			if f[i] < '0' || f[i] > '9' {
				return 0, fmt.Errorf("%w: %s %q", ErrTimeSyntax, kind, f)
			}
		}
	}
	h, _ := strconv.Atoi(hh)
	m, _ := strconv.Atoi(mm)
	sec, _ := strconv.Atoi(ss)
	millis, _ := strconv.Atoi(ms)
	if m > 59 || sec > 59 || millis > 999 {
		return 0, fmt.Errorf("%w: %s %02d:%02d:%02d.%03d", ErrTimeRange, kind, h, m, sec, millis)
	}
	return Tick(int64((h*3600+m*60+sec)*1000+millis) * (TickRate / 1000)), nil
}

// CueRange validates a start/end pair and reports negative durations.
func CueRange(start, end Tick) error {
	if end < start {
		return fmt.Errorf("%w: start=%s end=%s", ErrNegativeDuration, start, end)
	}
	return nil
}
