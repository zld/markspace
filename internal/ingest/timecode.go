package ingest

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"mediad/internal/core"
)

// FrameRate describes one of the supported timecode label rates.
type FrameRate struct {
	Name string // "24", "25", "29.97"
	Num  int64  // frames per second numerator
	Den  int64  // frames per second denominator
	Drop bool   // drop-frame labelling allowed
	Size int64  // nominal timecode frame-box (24, 25 or 30)
}

// Supported timecode rates. 29.97 is exactly 30000/1001.
var (
	Rate24   = FrameRate{Name: "24", Num: 24, Den: 1, Size: 24}
	Rate25   = FrameRate{Name: "25", Num: 25, Den: 1, Size: 25}
	Rate2997 = FrameRate{Name: "29.97", Num: 30000, Den: 1001, Drop: true, Size: 30}
)

// Rates lists every supported timecode rate in canonical order.
func Rates() []FrameRate { return []FrameRate{Rate24, Rate25, Rate2997} }

// ParseRate resolves a textual rate such as "24", "25", "29.97", "30000/1001".
func ParseRate(s string) (FrameRate, error) {
	switch strings.TrimSpace(s) {
	case "24":
		return Rate24, nil
	case "25":
		return Rate25, nil
	case "29.97", "30", "30000/1001":
		return Rate2997, nil
	case "24000/1001", "23.976":
		return FrameRate{Name: "23.976", Num: 24000, Den: 1001, Size: 24}, nil
	}
	return FrameRate{}, fmt.Errorf("ingest: unsupported timecode rate %q", s)
}

// Seconds returns one frame duration in rational seconds.
func (f FrameRate) Seconds() core.Rational { return core.Rat(f.Den, f.Num) }

// Timecode is a parsed SMPTE label with its exact media-clock value.
type Timecode struct {
	Hours       int           `json:"hours"`
	Minutes     int           `json:"minutes"`
	Seconds     int           `json:"seconds"`
	Frames      int           `json:"frames"`
	Drop        bool          `json:"drop"`
	Rate        string        `json:"rate"`
	Top         core.Rational `json:"clock"`
	Span        core.Span     `json:"span"`
	Raw         string        `json:"raw"`
	FramesTotal int64         `json:"frames_total"`
}

// TimecodeOption tunes parsing.
type TimecodeOption func(*timecodeOpts)

type timecodeOpts struct {
	allowMissingHours bool
}

// AllowMissingHours accepts "MM:SS:FF" labels, common in sidecar CSVs.
func AllowMissingHours() TimecodeOption {
	return func(o *timecodeOpts) { o.allowMissingHours = true }
}

// ParseTimecode parses "HH:MM:SS:FF" or "HH:MM:SS;FF" for the given rate.
// Drop-frame is detected by the semicolon separator, and the ';' form is
// rejected for rates that do not support drop-frame. Drop-frame labels whose
// frame number cannot exist (every minute except multiples of ten, frames
// 00 and 01) are rejected as CatInvalidDropFrame.
func ParseTimecode(raw string, rate FrameRate, span core.Span, opts ...TimecodeOption) (Timecode, error) {
	o := timecodeOpts{}
	for _, fn := range opts {
		fn(&o)
	}
	s := strings.TrimSpace(raw)
	if s == "" {
		return Timecode{}, errTimecode(span, raw, "empty timecode")
	}
	drop := false
	if i := strings.IndexByte(s, ';'); i >= 0 {
		if !rate.Drop {
			return Timecode{}, errTimecode(span, raw, fmt.Sprintf("drop-frame label not allowed at %s fps", rate.Name))
		}
		drop = true
		// Normalize only the separator; any further ';' is a syntax error.
		if strings.Count(s, ";") > 1 {
			return Timecode{}, errTimecode(span, raw, "multiple drop-frame separators")
		}
		s = s[:i] + ":" + s[i+1:]
	}
	if strings.ContainsAny(s, ",.") {
		return Timecode{}, errTimecode(span, raw, "timecode must use ':' field separators")
	}
	fields := strings.Split(s, ":")
	if len(fields) == 3 && o.allowMissingHours {
		fields = append([]string{"0"}, fields...)
	}
	if len(fields) != 4 {
		return Timecode{}, errTimecode(span, raw, "timecode needs HH:MM:SS:FF")
	}
	values := make([]int, 4)
	for i, f := range fields {
		if f == "" || len(f) > 9 {
			return Timecode{}, errTimecode(span, raw, "timecode field is not a number")
		}
		if len(f) > 2 {
			return Timecode{}, errTimecode(span, raw, "timecode field has more than two digits")
		}
		for j := 0; j < len(f); j++ {
			if f[j] < '0' || f[j] > '9' {
				return Timecode{}, errTimecode(span, raw, "timecode field is not decimal")
			}
		}
		v, err := strconv.Atoi(f)
		if err != nil {
			return Timecode{}, errTimecode(span, raw, "timecode field overflow")
		}
		values[i] = v
	}
	tc := Timecode{
		Hours:   values[0],
		Minutes: values[1],
		Seconds: values[2],
		Frames:  values[3],
		Drop:    drop,
		Rate:    rate.Name,
		Span:    span,
		Raw:     raw,
	}
	if tc.Minutes > 59 || tc.Seconds > 59 {
		return Timecode{}, errTimecode(span, raw, "minutes or seconds out of range")
	}
	if int64(tc.Frames) >= rate.Size {
		return Timecode{}, errTimecode(span, raw, fmt.Sprintf("frame number %d out of range for %s", tc.Frames, rate.Name))
	}
	if drop && tc.Seconds == 0 && tc.Frames < 2 && tc.Minutes%10 != 0 {
		return Timecode{}, errTimecode(span, raw, "invalid drop-frame label: frames 00/01 do not exist at this minute")
	}
	frames, err := labelToFrames(tc, rate)
	if err != nil {
		return Timecode{}, errTimecode(span, raw, err.Error())
	}
	tc.FramesTotal = frames
	tc.Top = core.Rat(frames*rate.Den, rate.Num)
	return tc, nil
}

// labelToFrames converts a validated label to a frame count. Drop-frame
// labels skip two frame numbers every minute except each tenth minute.
func labelToFrames(tc Timecode, rate FrameRate) (int64, error) {
	nominal := int64(tc.Hours)*3600*rate.Size + int64(tc.Minutes)*60*rate.Size +
		int64(tc.Seconds)*rate.Size + int64(tc.Frames)
	if !tc.Drop {
		return nominal, nil
	}
	if rate.Num != 30000 || rate.Den != 1001 {
		return 0, errors.New("drop-frame is only defined for 29.97")
	}
	totalMinutes := int64(tc.Hours)*60 + int64(tc.Minutes)
	dropped := 2 * (totalMinutes - totalMinutes/10)
	return nominal - dropped, nil
}

// FramesToTimecode renders a frame count back into a label. For drop-frame it
// reinserts the skipped frame numbers so the round trip is exact.
func FramesToTimecode(frames int64, rate FrameRate, drop bool) string {
	if frames < 0 {
		frames = 0
	}
	if drop {
		perMinute := int64(rate.Size)*60 - 2 // 1798 frames actually labelled per minute
		per10 := perMinute*10 + 18           // 17982 labelled frames per ten minutes
		d := frames / per10
		m := frames % per10
		if m > 2 {
			frames += 18*d + 2*((m-2)/perMinute)
		} else {
			frames += 18 * d
		}
	}
	ff := frames % rate.Size
	s := frames / rate.Size
	ss := s % 60
	mm := (s / 60) % 60
	hh := s / 3600
	sep := ":"
	if drop {
		sep = ";"
	}
	return fmt.Sprintf("%02d:%02d:%02d%s%02d", hh, mm, ss, sep, ff)
}

// errTimecode builds an offset-carrying ingest error.
func errTimecode(span core.Span, raw, detail string) error {
	return &TimecodeError{Span: span, Raw: raw, Detail: detail}
}

// TimecodeError reports an unparsable timecode label at its input span.
type TimecodeError struct {
	Span   core.Span
	Raw    string
	Detail string
}

func (e *TimecodeError) Error() string {
	return fmt.Sprintf("timecode [%d,%d) %q: %s", e.Span.Start, e.Span.End, e.Raw, e.Detail)
}
