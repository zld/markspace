package timebase

import (
	"fmt"
	"strconv"
	"strings"

	"mediarc/model"
)

// Rate describes a frame rate as the rational Num/Den and whether drop-frame
// numbering applies.
type Rate struct {
	Num       int64 // frames per second numerator
	Den       int64 // frames per second denominator
	DropFrame bool
}

// NewRate validates and builds a frame rate.
func NewRate(num, den int64, dropFrame bool) (Rate, error) {
	if num <= 0 || den <= 0 {
		return Rate{}, model.New(model.CodeInvalidTimecode, stream,
			fmt.Sprintf("frame rate must be positive, got %d/%d", num, den))
	}
	r := Rate{Num: num, Den: den, DropFrame: dropFrame}
	if dropFrame && !r.is29_97() {
		return Rate{}, model.New(model.CodeIllegalDropFrame, stream,
			fmt.Sprintf("drop-frame numbering is only defined for 29.97 fps, got %s", r))
	}
	return r, nil
}

// FPS24 returns exactly 24 fps.
func FPS24() Rate { return Rate{Num: 24, Den: 1} }

// FPS25 returns exactly 25 fps.
func FPS25() Rate { return Rate{Num: 25, Den: 1} }

// FPS50 returns exactly 50 fps.
func FPS50() Rate { return Rate{Num: 50, Den: 1} }

// FPS2997NDF returns 29.97 fps with non-drop-frame numbering.
func FPS2997NDF() Rate { return Rate{Num: 30000, Den: 1001} }

// FPS2997DF returns 29.97 fps with drop-frame numbering.
func FPS2997DF() Rate { return Rate{Num: 30000, Den: 1001, DropFrame: true} }

func (r Rate) is29_97() bool { return r.Num == 30000 && r.Den == 1001 }

// NominalFPS is the whole frame number used in timecode labels: 24, 25, 30, 50.
func (r Rate) NominalFPS() int64 { return (r.Num + r.Den/2) / r.Den }

// String renders the rate in a form ParseRate accepts.
func (r Rate) String() string {
	var base string
	switch {
	case r.is29_97():
		base = "29.97"
	case r.Den == 1:
		base = strconv.FormatInt(r.Num, 10)
	default:
		base = fmt.Sprintf("%d/%d", r.Num, r.Den)
	}
	if r.DropFrame {
		return base + "df"
	}
	return base
}

// ParseRate accepts "24", "25", "29.97", "29.97df", "29.97ndf", "50", as well
// as rational spellings such as "30000/1001".
func ParseRate(text string) (Rate, error) {
	normalized := strings.ToLower(strings.TrimSpace(text))
	drop := false
	switch {
	case strings.HasSuffix(normalized, "ndf"):
		normalized = strings.TrimSuffix(normalized, "ndf")
	case strings.HasSuffix(normalized, "df"):
		drop = true
		normalized = strings.TrimSuffix(normalized, "df")
	}
	switch normalized {
	case "24":
		return Rate{Num: 24, Den: 1, DropFrame: drop}, validateDrop(drop)
	case "25":
		return Rate{Num: 25, Den: 1, DropFrame: drop}, validateDrop(drop)
	case "29.97", "29.97ndf", "30000/1001":
		return Rate{Num: 30000, Den: 1001, DropFrame: drop}, nil
	case "50":
		return Rate{Num: 50, Den: 1, DropFrame: drop}, validateDrop(drop)
	}
	if num, den, ok := strings.Cut(normalized, "/"); ok {
		n, err1 := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
		d, err2 := strconv.ParseInt(strings.TrimSpace(den), 10, 64)
		if err1 == nil && err2 == nil {
			return NewRate(n, d, drop)
		}
	}
	return Rate{}, model.New(model.CodeUnsupportedFormat, stream,
		fmt.Sprintf("unsupported frame rate %q", text))
}

func validateDrop(drop bool) error {
	if drop {
		return model.New(model.CodeIllegalDropFrame, stream, "drop-frame numbering requires 29.97 fps")
	}
	return nil
}
