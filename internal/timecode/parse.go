// Package timecode parses SMPTE-style timecodes into the exact media clock.
package timecode

import (
	"fmt"
	"strconv"
	"strings"

	"mediaaudit/internal/model"
)

// Mode is one timecode system: a nominal frame count per timecode second plus
// the exact real-time rate it maps to.
type Mode struct {
	Name      string
	Nominal   int // frames counted per timecode second (24, 25, 30, 50)
	Rate      model.Rat
	DropFrame bool
}

// Supported modes. 29.97 is 30000/1001; drop-frame only applies to it.
var (
	Mode24      = Mode{Name: "24", Nominal: 24, Rate: model.MustRat(24, 1)}
	Mode25      = Mode{Name: "25", Nominal: 25, Rate: model.MustRat(25, 1)}
	Mode2997NDF = Mode{Name: "29.97ndf", Nominal: 30, Rate: model.MustRat(30000, 1001)}
	Mode2997DF  = Mode{Name: "29.97df", Nominal: 30, Rate: model.MustRat(30000, 1001), DropFrame: true}
	Mode50      = Mode{Name: "50", Nominal: 50, Rate: model.MustRat(50, 1)}
)

// ModeByName resolves a CLI/config mode label.
func ModeByName(name string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "24":
		return Mode24, true
	case "25":
		return Mode25, true
	case "29.97", "29.97ndf", "29.97-ndf", "30":
		return Mode2997NDF, true
	case "29.97df", "29.97-df", "drop":
		return Mode2997DF, true
	case "50":
		return Mode50, true
	}
	return Mode{}, false
}

// Timecode is a validated label plus the exact instant it denotes.
type Timecode struct {
	Mode   Mode
	Hours  int
	Minute int
	Second int
	Frame  int
	Total  int64 // absolute frame count, including dropped labels
	Time   model.Rat
}

// ParseModeName parses s (or "<mode>/HH:MM:SS:FF") after resolving the mode.
func ParseModeName(name string) (Mode, error) {
	m, ok := ModeByName(name)
	if !ok {
		return Mode{}, model.NewError(model.CodeInvalidMode, "timecode", "", model.InputPos{},
			"unsupported timecode mode %q (want 24, 25, 29.97ndf, 29.97df or 50)", name)
	}
	return m, nil
}

// Parse parses s using mode. pos supplies the file/line and the column where s
// begins; returned errors point at the offending component.
func Parse(s string, mode Mode, pos model.InputPos) (Timecode, error) {
	raw := s
	baseCol := pos.Column
	lead := len(s) - len(strings.TrimLeft(s, " \t"))
	if lead > 0 {
		if baseCol > 0 {
			baseCol += lead
		}
		s = s[lead:]
	}
	s = strings.TrimRight(s, " \t")

	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
		if baseCol > 0 {
			baseCol++
		}
	}
	sep, parts, err := splitTimecode(s)
	if err != nil {
		return Timecode{}, model.NewError(model.CodeInvalidTimecode, "timecode", "",
			pos, "invalid timecode %q: %v", raw, err)
	}
	if sep == ';' && !mode.DropFrame {
		return Timecode{}, model.NewError(model.CodeInvalidMode, "timecode", "",
			pos, "drop-frame separator ';' requires 29.97df mode, got %s", mode.Name)
	}
	if sep == ':' && mode.DropFrame {
		return Timecode{}, model.NewError(model.CodeInvalidMode, "timecode", "",
			pos, "mode %s requires the drop-frame separator ';' in %q", mode.Name, raw)
	}

	nums := make([]int, 4)
	for i, p := range parts {
		v, perr := strconv.Atoi(p.text)
		if perr != nil {
			col := baseCol + p.start
			return Timecode{}, model.NewError(model.CodeInvalidTimecode, "timecode", "",
				withCol(pos, col), "invalid numeric component %q in %q", p.text, raw)
		}
		nums[i] = v
	}
	h, m, sec, f := nums[0], nums[1], nums[2], nums[3]
	frameStartCol := baseCol + parts[3].start

	switch {
	case h < 0:
		return Timecode{}, model.NewError(model.CodeInvalidTimecode, "timecode", "", pos,
			"negative hours in %q", raw)
	case m >= 60 || sec >= 60:
		return Timecode{}, model.NewError(model.CodeInvalidTimecode, "timecode", "",
			withCol(pos, baseCol+parts[1].start),
			"minutes and seconds must be 00..59 in %q", raw)
	case f >= mode.Nominal:
		return Timecode{}, model.NewError(model.CodeInvalidFrameNumber, "timecode", "",
			withCol(pos, frameStartCol),
			"frame number %02d out of range for %s (00..%02d)", f, mode.Name, mode.Nominal-1)
	}

	tc := Timecode{Mode: mode, Hours: h, Minute: m, Second: sec, Frame: f}
	if mode.DropFrame {
		totalMinutes := int64(h*60 + m)
		if sec == 0 && f < 2 && m%10 != 0 {
			return Timecode{}, model.NewError(model.CodeInvalidFrameNumber, "timecode", "",
				withCol(pos, frameStartCol),
				"drop-frame timecode %q names dropped frames 00/01 at minute %d", raw, m)
		}
		tc.Total = int64(h)*108000 + int64(m)*1800 + int64(sec)*30 + int64(f) -
			2*(totalMinutes-totalMinutes/10)
	} else {
		tc.Total = (int64(h)*3600+int64(m)*60+int64(sec))*int64(mode.Nominal) + int64(f)
	}
	if neg {
		tc.Total = -tc.Total
	}
	tc.Time, err = framesToTime(tc.Total, mode)
	if err != nil {
		return Timecode{}, err
	}
	return tc, nil
}

func framesToTime(frames int64, mode Mode) (model.Rat, error) {
	r, err := model.NewRat(frames, 1)
	if err != nil {
		return model.Rat{}, err
	}
	t, err := r.Quo(mode.Rate)
	if err != nil {
		return model.Rat{}, model.NewError(model.CodeInternal, "timecode", "", model.InputPos{},
			"invalid rate %s: %v", mode.Rate, err)
	}
	return t, nil
}

// FromFrames converts an absolute frame count to a timecode, applying the
// drop-frame label rules. It is the inverse of Parse for valid frame counts.
func FromFrames(frames int64, mode Mode) (Timecode, error) {
	neg := frames < 0
	abs := frames
	if neg {
		abs = -abs
	}
	t := Timecode{Mode: mode, Total: frames}
	var err error
	t.Time, err = framesToTime(frames, mode)
	if err != nil {
		return Timecode{}, err
	}
	if mode.DropFrame {
		t.Hours, t.Minute, t.Second, t.Frame = dfFields(abs)
	} else {
		nom := int64(mode.Nominal)
		t.Hours = int(abs / (3600 * nom))
		abs %= 3600 * nom
		t.Minute = int(abs / (60 * nom))
		abs %= 60 * nom
		t.Second = int(abs / nom)
		t.Frame = int(abs % nom)
	}
	if neg {
		t.Hours, t.Minute, t.Second, t.Frame = -t.Hours, -t.Minute, -t.Second, -t.Frame
	}
	return t, nil
}

func dfFields(frames int64) (h, m, s, f int) {
	// Minute 0 of each 10-minute block has 1800 frames; minutes 1..9 have
	// 1798 each because labels ;00 and ;01 are skipped. Total 17982.
	const perBlock, firstMinute, perMinute = int64(17982), int64(1800), int64(1798)
	block := frames / perBlock
	rem := frames % perBlock
	var minuteInBlock, labelFrame int64
	if rem < firstMinute {
		labelFrame = rem
	} else {
		rem -= firstMinute
		minuteInBlock = 1 + rem/perMinute
		labelFrame = rem%perMinute + 2
	}
	totalMinutes := block*10 + minuteInBlock
	h = int(totalMinutes / 60)
	m = int(totalMinutes % 60)
	s = int(labelFrame / 30)
	f = int(labelFrame % 30)
	return
}

type part struct {
	text  string
	start int
}

// splitTimecode validates the shape HH:MM:SS<sep>FF and returns byte offsets
// of each component relative to s.
func splitTimecode(s string) (byte, []part, error) {
	var parts []part
	start := 0
	var sep byte
	var fieldSep byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			continue
		}
		if c == ':' || c == ';' {
			if len(parts) == 3 {
				return 0, nil, fmt.Errorf("too many separators")
			}
			if i == start {
				return 0, nil, fmt.Errorf("empty component")
			}
			// The first three components are separated by the field
			// separator; only the final one (before frames) may differ, and
			// it carries the drop-frame marker.
			if len(parts) == 0 {
				fieldSep = c
			} else if len(parts) < 2 && c != fieldSep {
				return 0, nil, fmt.Errorf("inconsistent separators %q and %q", string(fieldSep), string(c))
			}
			parts = append(parts, part{text: s[start:i], start: start})
			sep = c
			start = i + 1
			continue
		}
		return 0, nil, fmt.Errorf("unexpected character %q", c)
	}
	if len(parts) != 3 {
		return 0, nil, fmt.Errorf("want HH:MM:SS<sep>FF, got %d components", len(parts)+1)
	}
	if start >= len(s) {
		return 0, nil, fmt.Errorf("missing frame number")
	}
	parts = append(parts, part{text: s[start:], start: start})
	return sep, parts, nil
}

// String renders the canonical label (hours at least two digits).
func (t Timecode) String() string {
	sep := ":"
	if t.Mode.DropFrame {
		sep = ";"
	}
	sign := ""
	if t.Total < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s%02d:%02d:%02d%s%02d", sign, abs(t.Hours), abs(t.Minute), abs(t.Second), sep, abs(t.Frame))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func withCol(pos model.InputPos, col int) model.InputPos {
	if col > 0 {
		pos.Column = col
	}
	return pos
}
