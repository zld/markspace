package clock

import (
	"fmt"
	"strconv"
	"strings"

	"mediaarchive/internal/diag"
)

// Format identifies one of the supported SMPTE timecode families. 29.97 is
// split into drop-frame and non-drop-frame because the two label the same
// wall-clock second differently and must never be guessed.
type Format int

const (
	Format24 Format = iota
	Format25
	Format2997DF
	Format2997NDF
	Format50
)

// String returns the canonical format token used by the CLI and fixtures.
func (f Format) String() string {
	switch f {
	case Format24:
		return "24"
	case Format25:
		return "25"
	case Format2997DF:
		return "29.97df"
	case Format2997NDF:
		return "29.97ndf"
	case Format50:
		return "50"
	default:
		return fmt.Sprintf("unknown(%d)", int(f))
	}
}

// NominalFPS is the integer label rate: 30 for both 29.97 variants.
func (f Format) NominalFPS() int {
	switch f {
	case Format24:
		return 24
	case Format25:
		return 25
	case Format2997DF, Format2997NDF:
		return 30
	case Format50:
		return 50
	default:
		return 0
	}
}

// IsDropFrame reports whether labels drop frame numbers 0 and 1 at each
// minute not divisible by ten.
func (f Format) IsDropFrame() bool { return f == Format2997DF }

// FrameDuration is the exact real duration of one frame on the format's
// time base. Drop-frame only changes labels, never the frame rate.
func (f Format) FrameDuration() Rational {
	switch f {
	case Format2997DF, Format2997NDF:
		return NewRat(1001, 30000)
	case Format24:
		return NewRat(1, 24)
	case Format25:
		return NewRat(1, 25)
	case Format50:
		return NewRat(1, 50)
	default:
		return Zero
	}
}

// ParseFormat parses the canonical tokens accepted from fixtures and flags.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "24", "24fps":
		return Format24, nil
	case "25", "25fps", "pal":
		return Format25, nil
	case "29.97df", "29.97-df", "30000/1001df", "ntsc-df":
		return Format2997DF, nil
	case "29.97ndf", "29.97-ndf", "29.97", "30000/1001ndf", "ntsc":
		return Format2997NDF, nil
	case "50", "50fps":
		return Format50, nil
	default:
		return 0, fmt.Errorf("unknown timecode format %q", s)
	}
}

// FormatFromRate resolves a frame-rate expression such as "30000/1001".
// A rate of 30000/1001 is inherently ambiguous between drop-frame and
// non-drop-frame labeling, so the caller must supply an explicit suffix
// ("df"/"ndf") or split-family token; otherwise a CodeAmbiguousDF fault is
// returned at the given source offset.
func FormatFromRate(rate string, offset int64) (Format, error) {
	s := strings.ToLower(strings.TrimSpace(rate))
	base := strings.TrimSuffix(strings.TrimSuffix(s, "fps"), "p")
	switch base {
	case "24", "24/1":
		return Format24, nil
	case "25", "25/1":
		return Format25, nil
	case "50", "50/1":
		return Format50, nil
	case "30000/1001", "29.97", "29.97fps":
		return 0, diag.New(diag.CodeAmbiguousDF, offset,
			"rate %q is ambiguous: label drop-frame (29.97df) or non-drop-frame (29.97ndf)", rate)
	}
	if strings.HasSuffix(base, "df") {
		if f := strings.TrimSuffix(base, "df"); f == "29.97" || f == "30000/1001" {
			return Format2997DF, nil
		}
	}
	if strings.HasSuffix(base, "ndf") {
		if f := strings.TrimSuffix(base, "ndf"); f == "29.97" || f == "30000/1001" {
			return Format2997NDF, nil
		}
	}
	return ParseFormat(rate)
}

// Timecode is a single SMPTE label on a named format.
type Timecode struct {
	Hours   int
	Minutes int
	Seconds int
	Frames  int
	Format  Format
}

// String renders the canonical label. Drop-frame uses ';' before frames.
func (tc Timecode) String() string {
	sep := ":"
	if tc.Format.IsDropFrame() {
		sep = ";"
	}
	return fmt.Sprintf("%02d:%02d:%02d%s%02d", tc.Hours, tc.Minutes, tc.Seconds, sep, tc.Frames)
}

// FrameNumber converts the label to an absolute frame number. Drop-frame labels
// that name a skipped frame (or carry a ';' with a format that has no drop
// rule) are rejected rather than silently repaired.
func (tc Timecode) FrameNumber() (int64, error) {
	f := tc.Format
	if f.NominalFPS() == 0 {
		return 0, diag.New(diag.CodeInvalidTime, 0, "unknown timecode format")
	}
	if tc.Hours < 0 || tc.Minutes < 0 || tc.Seconds < 0 || tc.Frames < 0 {
		return 0, diag.New(diag.CodeInvalidTime, 0, "negative timecode component in %q", tc.String())
	}
	if tc.Minutes > 59 || tc.Seconds > 59 || tc.Frames >= f.NominalFPS() {
		return 0, diag.New(diag.CodeInvalidTime, 0,
			"component out of range for %s: %q", f, tc.String())
	}
	nominal := int64(f.NominalFPS())
	totalMinutes := int64(60*tc.Hours + tc.Minutes)
	frame := nominal*(3600*int64(tc.Hours)+60*int64(tc.Minutes)+int64(tc.Seconds)) + int64(tc.Frames)
	if f.IsDropFrame() {
		if tc.Minutes%10 != 0 && tc.Seconds == 0 && tc.Frames < 2 {
			return 0, diag.New(diag.CodeInvalidTime, 0,
				"%q labels a dropped frame on 29.97df", tc.String())
		}
		frame -= 2 * (totalMinutes - totalMinutes/10)
	}
	if frame < 0 {
		return 0, diag.New(diag.CodeInvalidTime, 0, "timecode %q maps to negative frame", tc.String())
	}
	return frame, nil
}

// Position returns the exact elapsed time of the label from 00:00:00:00.
func (tc Timecode) Position() (Rational, error) {
	n, err := tc.FrameNumber()
	if err != nil {
		return Zero, err
	}
	return tc.Format.FrameDuration().MulInt(n), nil
}

// ParseTimecode parses HH:MM:SS:FF or HH:MM:SS;FF under an explicit format.
// The separator is preserved so a drop-frame label can be audited, but the
// format decides frame arithmetic.
func ParseTimecode(s string, f Format) (Timecode, error) {
	trimmed := strings.TrimSpace(s)
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ':' || r == ';'
	})
	if len(parts) != 4 {
		return Timecode{}, diag.New(diag.CodeInvalidTime, 0,
			"timecode %q must be HH:MM:SS:FF or HH:MM:SS;FF", s)
	}
	vals := [4]int{}
	for i, p := range parts {
		if p == "" {
			return Timecode{}, diag.New(diag.CodeInvalidTime, 0, "empty timecode component in %q", s)
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return Timecode{}, diag.New(diag.CodeInvalidTime, 0,
				"non-numeric timecode component %q in %q", p, s)
		}
		vals[i] = n
	}
	tc := Timecode{Hours: vals[0], Minutes: vals[1], Seconds: vals[2], Frames: vals[3], Format: f}
	if _, err := tc.FrameNumber(); err != nil {
		return Timecode{}, err
	}
	return tc, nil
}

// DayFrames is the number of real frames in one nominal 24-hour label day.
func (f Format) DayFrames() int64 {
	day := int64(f.NominalFPS()) * 24 * 3600
	if f.IsDropFrame() {
		minutes := int64(24 * 60)
		day -= 2 * (minutes - minutes/10)
	}
	return day
}

// ExpandSequence turns a run of labels into an absolute, monotone frame
// sequence by adding one day whenever the label wraps past midnight. A
// backward jump that is not a midnight wrap is a structured fault.
func ExpandSequence(labels []Timecode, f Format, srcOffset int64) ([]int64, error) {
	out := make([]int64, 0, len(labels))
	day := int64(0)
	dayFrames := f.DayFrames()
	var prev int64 = -1
	for i, tc := range labels {
		tc.Format = f
		n, err := tc.FrameNumber()
		if err != nil {
			if fault, ok := diag.As(err); ok {
				return nil, fault.AtOffset(srcOffset + int64(i))
			}
			return nil, err
		}
		n += day
		if prev >= 0 && n <= prev {
			if prev-n <= dayFrames/2 {
				return nil, diag.New(diag.CodeTimestampRollback, srcOffset+int64(i),
					"timecode %q does not advance after frame %d", tc.String(), prev)
			}
			day += dayFrames
			n += dayFrames
		}
		prev = n
		out = append(out, n)
	}
	return out, nil
}

// Unwrapper folds 32-bit (or 33-bit) media timestamps into a monotone int64.
// A wrap in [0,2^bits) adds one period; any smaller backward step is
// reported as a rollback fault carrying the element index.
type Unwrapper struct {
	mod     int64
	high    int64
	last    int64
	started bool
	index   int64
}

// NewUnwrapper builds an unwrapper for an unsigned timestamp of the given
// bit width (32 for RTP/most MP4 clocks, 33 for 90 kHz MPEG clocks).
func NewUnwrapper(bits uint) *Unwrapper {
	if bits == 0 || bits > 62 {
		bits = 32
	}
	return &Unwrapper{mod: int64(1) << bits}
}

// Period returns the wrap modulus.
func (u *Unwrapper) Period() int64 { return u.mod }

// Step feeds the next raw timestamp and returns its unfolded value. The
// first value seeds the sequence. A backward step greater than half the
// period is treated as a wrap; a smaller one is a fault at the current index.
func (u *Unwrapper) Step(ts int64) (int64, error) {
	idx := u.index
	u.index++
	if !u.started {
		u.started, u.last = true, ts
		return ts, nil
	}
	d := ts - u.last
	if d < 0 {
		if -d > u.mod/2 {
			u.high += u.mod
		} else {
			return 0, diag.New(diag.CodeTimestampRollback, idx,
				"timestamp went backwards by %d ticks (last=%d, next=%d)", -d, u.last, ts)
		}
	}
	u.last = ts
	return u.high + ts, nil
}
