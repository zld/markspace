// Package clock provides one exact integer time base shared by subtitles,
// streaming, MP4 boundaries, and recovery checkpoints.
package clock

import (
	"fmt"
	"strconv"
	"strings"

	"mediapack/internal/fault"
)

// TicksPerSecond is divisible by every supported frame duration and by
// subtitle milliseconds. Supported per-frame tick counts are:
//
//	24 fps       75000
//	25 fps       72000
//	29.97 fps     60060 (30000/1001)
//	50 fps       36000
const TicksPerSecond int64 = 1_800_000

// Ticks is an integer position in the shared time base.
type Ticks int64

// Milliseconds converts ticks to floating-point milliseconds for reports.
func (t Ticks) Milliseconds() float64 {
	return float64(t) * 1000 / float64(TicksPerSecond)
}

// Rate describes a timecode frame rate. NominalFPS is the counting rate used
// by SMPTE timecode (30 for 29.97).
type Rate struct {
	Name       string
	NominalFPS int
	Drop       bool
}

var (
	Rate24     = Rate{Name: "24", NominalFPS: 24}
	Rate25     = Rate{Name: "25", NominalFPS: 25}
	Rate2997DF = Rate{Name: "29.97DF", NominalFPS: 30, Drop: true}
	Rate2997ND = Rate{Name: "29.97NDF", NominalFPS: 30, Drop: false}
	Rate50     = Rate{Name: "50", NominalFPS: 50}
)

// ParseRate accepts the common command-line spellings.
func ParseRate(s string) (Rate, error) {
	switch strings.ToLower(strings.ReplaceAll(s, " ", "")) {
	case "24", "24fps":
		return Rate24, nil
	case "25", "25fps":
		return Rate25, nil
	case "29.97df", "2997df":
		return Rate2997DF, nil
	case "29.97ndf", "2997ndf", "29.97":
		return Rate2997ND, nil
	case "50", "50fps":
		return Rate50, nil
	default:
		return Rate{}, fault.New(fault.InvalidTimecode, "unsupported frame rate %q", s)
	}
}

// FrameDuration returns the exact integer duration of one output frame.
func (r Rate) FrameDuration() Ticks {
	switch r.Name {
	case "24":
		return Ticks(TicksPerSecond / 24)
	case "25":
		return Ticks(TicksPerSecond / 25)
	case "29.97DF", "29.97NDF":
		// One frame is 1001/30000 second.
		return Ticks(TicksPerSecond * 1001 / 30000)
	case "50":
		return Ticks(TicksPerSecond / 50)
	default:
		return 0
	}
}

// FrameToTicks converts a real frame index. For DF rates the index is the
// compact/drop-frame-adjusted frame number.
func (r Rate) FrameToTicks(frameIndex int64) Ticks {
	return Ticks(frameIndex) * r.FrameDuration()
}

// Timecode is a parsed SMPTE timecode.
type Timecode struct {
	Hours      int
	Minutes    int
	Seconds    int
	Frames     int
	FrameIndex int64
	Ticks      Ticks
	DropFrame  bool
}

// ParseTimecode parses HH:MM:SS:FF or HH:MM:SS;FF. baseOffset is the byte
// offset of this timecode inside source and is included in structured errors.
func ParseTimecode(source string, baseOffset int, rate Rate, raw string) (Timecode, error) {
	if strings.HasPrefix(raw, "-") {
		return Timecode{}, fault.At(fault.NegativeTime, source, int64(baseOffset),
			"negative timecode %q is not allowed", raw)
	}
	if len(raw) < 11 {
		return Timecode{}, fault.At(fault.InvalidTimecode, source, int64(baseOffset),
			"timecode %q is too short", raw)
	}
	// Expected positions: 2,5 separators and either ':' or ';' at 8.
	if raw[2] != ':' || raw[5] != ':' {
		return Timecode{}, fault.At(fault.InvalidTimecode, source, int64(baseOffset+2),
			"timecode %q must use HH:MM:SS:FF", raw)
	}
	sep := raw[8]
	dropMarker := sep == ';' || sep == '.'
	if sep != ':' && !dropMarker {
		return Timecode{}, fault.At(fault.InvalidDropMarker, source, int64(baseOffset+8),
			"invalid drop-frame marker %q in %q", string(sep), raw)
	}
	if dropMarker && !rate.Drop {
		return Timecode{}, fault.At(fault.InvalidDropMarker, source, int64(baseOffset+8),
			"drop-frame marker used with non-drop rate %s", rate.Name)
	}
	if rate.Drop && sep == ':' {
		return Timecode{}, fault.At(fault.InvalidDropMarker, source, int64(baseOffset+8),
			"29.97 DF timecode requires ';' frame separator in %q", raw)
	}
	parts := []string{raw[0:2], raw[3:5], raw[6:8], raw[9:]}
	nums := make([]int, 4)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || len(p) != 2 {
			return Timecode{}, fault.At(fault.InvalidTimecode, source,
				int64(baseOffset), "invalid timecode field %q", p)
		}
		nums[i] = n
	}
	h, m, s, f := nums[0], nums[1], nums[2], nums[3]
	if h < 0 || h > 23 {
		return Timecode{}, fault.At(fault.OutOfRange, source, int64(baseOffset),
			"hours %d out of range 0..23 in %q", h, raw)
	}
	if m > 59 || s > 59 {
		return Timecode{}, fault.At(fault.OutOfRange, source, int64(baseOffset+3),
			"minutes/seconds out of range in %q", raw)
	}
	if f >= rate.NominalFPS {
		return Timecode{}, fault.At(fault.OutOfRange, source, int64(baseOffset+9),
			"frame %d out of range for %s", f, rate.Name)
	}
	if rate.Drop && m%10 != 0 && s == 0 && f < 2 {
		return Timecode{}, fault.At(fault.InvalidDropMarker, source, int64(baseOffset+9),
			"frame %d was dropped at %02d:%02d for 29.97 DF", f, m, s)
	}

	nominal := int64(((h*60+m)*60+s)*rate.NominalFPS + f)
	frameIndex := nominal
	if rate.Drop {
		totalMinutes := int64(h*60 + m)
		frameIndex = nominal - 2*totalMinutes + 2*(totalMinutes/10)
	}
	return Timecode{
		Hours: h, Minutes: m, Seconds: s, Frames: f,
		FrameIndex: frameIndex,
		Ticks:      rate.FrameToTicks(frameIndex),
		DropFrame:  rate.Drop,
	}, nil
}

// String renders the timecode using the correct DF separator.
func (t Timecode) String() string {
	sep := ":"
	if t.DropFrame {
		sep = ";"
	}
	return fmt.Sprintf("%02d:%02d:%02d%s%02d", t.Hours, t.Minutes, t.Seconds, sep, t.Frames)
}

// CheckNonNegative rejects negative shared-clock positions without clamping.
func CheckNonNegative(source string, offset int64, t Ticks) error {
	if t < 0 {
		return fault.At(fault.NegativeTime, source, offset, "time %d ticks is negative", t)
	}
	return nil
}
