// Package timecode parses the SMPTE-style timecodes used by archive subtitle
// and stream metadata and converts them onto the shared nanosecond scale.
package timecode

import (
	"strconv"
	"strings"
	"unicode"

	"archivecheck/internal/fault"
	"archivecheck/internal/timebase"
)

// Value is a parsed timecode. Frames is the real frame index since midnight
// with drop-frame labels already accounted for; Nanos is the same instant on
// the shared scale.
type Value struct {
	Text   string
	Rate   timebase.Rate
	DF     bool
	Frames int64
	Nanos  timebase.Nanos
}

// RateFor returns the timebase rate for a nominal frame-per-second count.
// Only 24, 25, 29.97 and 50 are accepted; 29.97 is represented as 30000/1001.
func RateFor(fps string) (timebase.Rate, bool) {
	switch fps {
	case "24", "23.976":
		return timebase.FPS24, fps == "24"
	case "25":
		return timebase.FPS25, true
	case "29.97", "30":
		return timebase.FPS2997, true
	case "50":
		return timebase.FPS50, true
	}
	return timebase.Rate{}, false
}

// Parse parses one timecode string. The separator before the frame field
// selects counting mode: ':' is non-drop-frame, ';' (or ',') is drop-frame.
// Drop-frame is only legal for 29.97. Malformed fields, negative values,
// labels inside a dropped range and values beyond 24 h all produce an
// offset-carrying error; nothing is silently clamped.
func Parse(text string, rate timebase.Rate) (Value, error) {
	return ParseAt(text, rate, 0)
}

// ParseAt is Parse for a timecode found at base within a larger source. The
// error offset is base plus the index of the offending character.
func ParseAt(text string, rate timebase.Rate, base int64) (Value, error) {
	v := Value{Text: text, Rate: rate}
	if !rate.Supported() {
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base, "unsupported frame rate %d/%d", rate.Num, rate.Den)
	}
	trimmed := strings.TrimSpace(text)
	lead := int64(strings.Index(text, trimmed))
	if trimmed == "" {
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base, "empty timecode")
	}
	if strings.HasPrefix(trimmed, "-") {
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead, "negative timecode %q", text)
	}
	if len(trimmed) < 11 {
		return v, fault.New(fault.StageTimecode, fault.KindTruncated, "", base+lead, "timecode %q is shorter than HH:MM:SS:FF", text)
	}
	sep := rune(trimmed[8])
	switch sep {
	case ';', ',':
		v.DF = true
	case ':':
		v.DF = false
	default:
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead+8, "timecode %q must separate frames with ':' or ';'", text)
	}
	if v.DF && !rate.DropFrame() {
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead+8, "drop-frame marker is only valid for 29.97, not %d/%d", rate.Num, rate.Den)
	}
	fields := [4]string{trimmed[0:2], trimmed[3:5], trimmed[6:8], trimmed[9:11]}
	bounds := [4]int64{0, 3, 6, 9}
	for i, f := range fields {
		if len(f) != 2 || !allDigits(f) {
			// Reports the first non-digit so a truncated file points at the cut.
			return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead+bounds[i], "timecode field %d (%q) is not two digits", i, f)
		}
	}
	if trimmed[2] != ':' || trimmed[5] != ':' {
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead+2, "timecode %q must use ':' between hours, minutes and seconds", text)
	}
	if len(trimmed) > 11 {
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead+11, "trailing data %q after timecode", trimmed[11:])
	}
	digits := func(i int) int64 {
		n, _ := strconv.ParseInt(fields[i], 10, 64)
		return n
	}
	hh, mm, ss, ff := digits(0), digits(1), digits(2), digits(3)
	nominal := rate.Nominal()
	if mm > 59 {
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead+3, "minutes %02d out of range", mm)
	}
	if ss > 59 {
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead+6, "seconds %02d out of range", ss)
	}
	if ff >= nominal {
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead+9, "frame %02d out of range for %d fps", ff, nominal)
	}
	// Per-second-wrapped label count; 24 h at 30 fps is 2,592,000 labels.
	labels := hh*3600*nominal + mm*60*nominal + ss*nominal + ff
	if labels > 99*3600*nominal {
		return v, fault.New(fault.StageTimecode, fault.KindOverflow, "", base+lead, "timecode %q exceeds the 99-hour format limit", text)
	}
	frames := labels
	if v.DF {
		minutes := hh*60 + mm
		if ss == 0 && ff < 2 && minutes%10 != 0 {
			return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead+6, "drop-frame label %q is a dropped frame number", text)
		}
		frames = labels - 2*(minutes-minutes/10)
	}
	if frames < 0 {
		return v, fault.New(fault.StageTimecode, fault.KindInvalid, "", base+lead, "timecode %q maps to a negative frame index", text)
	}
	nanos, err := rate.TimecodeToNanos(frames)
	if err != nil {
		return v, fault.Wrap(err, fault.StageTimecode, fault.KindOverflow, "", base+lead, "timecode %q", text)
	}
	if nanos > timebase.MaxNanos {
		return v, fault.New(fault.StageTimecode, fault.KindOverflow, "", base+lead, "timecode %q is beyond 24 hours", text)
	}
	v.Frames, v.Nanos = frames, nanos
	return v, nil
}

// FormatNanos renders n as an NDF HH:MM:SS:FF label at rate.
func FormatNanos(n timebase.Nanos, rate timebase.Rate) (string, error) {
	frames, err := rate.NanosToFrames(n)
	if err != nil {
		return "", err
	}
	nominal := rate.Nominal()
	ff := frames % nominal
	total := frames / nominal
	ss := total % 60
	mm := (total / 60) % 60
	hh := total / 3600
	two := func(v int64) string {
		s := strconv.FormatInt(v, 10)
		if len(s) < 2 {
			return "0" + s
		}
		return s
	}
	return two(hh) + ":" + two(mm) + ":" + two(ss) + ":" + two(ff), nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) || r > unicode.MaxASCII {
			return false
		}
	}
	return true
}
