// Package media defines the shared media-time model and structured error model
// used by every processing stage (subtitles, RTP, MP4, manifests).
package media

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Rat is an exact rational number (numerator/denominator), always reduced.
// It is used for all media timings so no floating-point rounding is ever
// introduced into the shared time base.
type Rat struct {
	num int64
	den int64 // strictly positive
}

// NewRat builds a reduced Rat. It panics when den is zero.
func NewRat(num, den int64) Rat {
	if den == 0 {
		panic("media: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 {
		return Rat{0, 1}
	}
	g := gcd(abs64(num), den)
	return Rat{num / g, den / g}
}

// NewInt returns an integer-valued Rat.
func NewInt(n int64) Rat { return Rat{n, 1} }

// Num returns the numerator.
func (r Rat) Num() int64 { return r.num }

// Den returns the denominator (always positive).
func (r Rat) Den() int64 { return r.den }

// IsZero reports whether the value is zero.
func (r Rat) IsZero() bool { return r.num == 0 }

// Cmp returns -1, 0 or 1 when r is less than, equal to or greater than o.
func (r Rat) Cmp(o Rat) int {
	ln := r.num * o.den
	rn := o.num * r.den
	switch {
	case ln < rn:
		return -1
	case ln > rn:
		return 1
	default:
		return 0
	}
}

// Add returns r+o as a reduced Rat.
func (r Rat) Add(o Rat) Rat { return NewRat(r.num*o.den+o.num*r.den, r.den*o.den) }

// Sub returns r-o as a reduced Rat.
func (r Rat) Sub(o Rat) Rat { return NewRat(r.num*o.den-o.num*r.den, r.den*o.den) }

// Mul returns r*o as a reduced Rat.
func (r Rat) Mul(o Rat) Rat { return NewRat(r.num*o.num, r.den*o.den) }

// Quo returns r/o as a reduced Rat. It panics when o is zero.
func (r Rat) Quo(o Rat) Rat { return NewRat(r.num*o.den, r.den*o.num) }

// Float64 returns a floating-point approximation.
func (r Rat) Float64() float64 { return float64(r.num) / float64(r.den) }

// String renders the value as "num/den" (or "n" for integers).
func (r Rat) String() string {
	if r.den == 1 {
		return strconv.FormatInt(r.num, 10)
	}
	return strconv.FormatInt(r.num, 10) + "/" + strconv.FormatInt(r.den, 10)
}

// FrameRate is a media frame rate expressed as an exact rational (frames/sec).
type FrameRate struct{ Rat }

// NewFrameRate builds a FrameRate from numerator and denominator.
func NewFrameRate(num, den int64) FrameRate { return FrameRate{NewRat(num, den)} }

// Common frame rates, including the fractional NTSC rate kept exact.
var (
	Rate24     = NewFrameRate(24, 1)
	Rate25     = NewFrameRate(25, 1)
	Rate2997   = NewFrameRate(30000, 1001) // 29.97... NDF base
	Rate30000  = NewFrameRate(30, 1)
	Rate50     = NewFrameRate(50, 1)
)

// Timecode carries a parsed wall-clock timecode in media time.
type Timecode struct {
	Start    Rat       // exact seconds on the unified media time line
	Frame    int       // frame index within the second (0-based)
	Rate     FrameRate // rate at which the timecode was expressed
	DropFrame bool     // SMPTE drop-frame counting (29.97 only)
}

// dropFramesPer10Min is the number of frame numbers skipped per 10 minutes
// under SMPTE drop-frame counting (2 frames per minute, except every tenth).
const dropFramesPer10Min = 18

// ParseTimecode parses "HH:MM:SS:FF" (NDF) and "HH:MM:SS;FF" (DF) timecodes.
// rate must be one of the supported rates; invalid frame numbers are rejected
// with a structured error rather than silently folded into the time line.
func ParseTimecode(s string, rate FrameRate) (Timecode, error) {
	parts := splitFields(s, []byte{':', ';'})
	if len(parts) != 4 {
		return Timecode{}, InvalidTime(s, "timecode must be HH:MM:SS:FF or HH:MM:SS;FF")
	}
	df := strings.ContainsRune(s, ';')
	h, e1 := parseUintField(parts[0])
	m, e2 := parseUintField(parts[1])
	sec, e3 := parseUintField(parts[2])
	ff, e4 := parseUintField(parts[3])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return Timecode{}, InvalidTime(s, "timecode fields must be non-negative integers")
	}
	if m > 59 || sec > 59 {
		return Timecode{}, InvalidTime(s, "minutes and seconds must be below 60")
	}
	if df && rate.Cmp(Rate2997.Rat) != 0 {
		return Timecode{}, InvalidTime(s, "drop-frame timecode is only valid at 29.97 fps")
	}
	// nominalFrames is the integer frame-numbering rate (0..nominal-1 per
	// second). For NTSC 29.97 the timecode still numbers 30 frames/second;
	// only the true media rate (30000/1001) is fractional.
	nominalFrames := nominalFrameCount(rate)
	if int64(ff) >= int64(nominalFrames) {
		return Timecode{}, InvalidTime(s, fmt.Sprintf(
			"frame %d exceeds rate %d fps", ff, nominalFrames))
	}
	// At the start of every minute except every tenth, frame numbers 0 and 1
	// are skipped (the ;00 and ;01 labels never exist). Seconds is 0 there.
	if df && m%10 != 0 && sec == 0 && (ff == 0 || ff == 1) {
		return Timecode{}, InvalidTime(s, fmt.Sprintf(
			"drop-frame timecode skips frame number %02d at this minute", ff))
	}
	totalFrames := int64(h*3600+m*60+sec)*int64(nominalFrames) + int64(ff)
	if df {
		dropped := int64(dropFramesPer10Min*(h*6+m/10) + 2*(m%10))
		totalFrames -= dropped
	}
	if totalFrames < 0 {
		return Timecode{}, InvalidTime(s, "timecode resolves to a negative media time")
	}
	// media seconds = frames / true fps, with true fps = num/den.
	start := NewRat(totalFrames*rate.den, rate.num)
	return Timecode{Start: start, Frame: ff, Rate: rate, DropFrame: df}, nil
}

// nominalFrameCount returns the integer frame-numbering count per second for
// a supported rate (24, 25, 29.97, 50).
func nominalFrameCount(rate FrameRate) int {
	switch rate {
	case Rate24:
		return 24
	case Rate25:
		return 25
	case Rate2997:
		return 30
	case Rate50:
		return 50
	default:
		// Fall back to the rounded rate numerator/denominator.
		return int((rate.num + rate.den/2) / rate.den)
	}
}

// ParseSRTTime parses an SRT timestamp "HH:MM:SS,mmm" into exact seconds.
func ParseSRTTime(s string) (Rat, error) {
	parts := splitFields(s, []byte{':'})
	if len(parts) != 3 || !strings.ContainsRune(s, ',') {
		return Rat{}, InvalidTime(s, "SRT time must be HH:MM:SS,mmm")
	}
	last := strings.SplitN(parts[2], ",", 2)
	if len(last) != 2 || len(last[1]) != 3 {
		return Rat{}, InvalidTime(s, "SRT milliseconds must be exactly 3 digits")
	}
	return hmsToTime(s, parts[0], parts[1], last[0], last[1], 1000)
}

// ParseVTTTime parses a WebVTT timestamp "HH:MM:SS.mmm" (hours optional)
// into exact seconds.
func ParseVTTTime(s string) (Rat, error) {
	fields := splitFields(s, []byte{':'})
	if len(fields) != 3 || !strings.ContainsRune(s, '.') {
		return Rat{}, InvalidTime(s, "VTT time must be MM:SS.mmm or HH:MM:SS.mmm")
	}
	last := strings.SplitN(fields[2], ".", 2)
	if len(last) != 2 || len(last[1]) != 3 {
		return Rat{}, InvalidTime(s, "VTT milliseconds must be exactly 3 digits")
	}
	return hmsToTime(s, fields[0], fields[1], last[0], last[1], 1000)
}

func hmsToTime(raw, hh, mm, ss, frac string, fracScale int64) (Rat, error) {
	h, e1 := parseUintField(hh)
	m, e2 := parseUintField(mm)
	sec, e3 := parseUintField(ss)
	fracV, e4 := parseUintField(frac)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return Rat{}, InvalidTime(raw, "time fields must be non-negative integers")
	}
	if m > 59 || sec > 59 {
		return Rat{}, InvalidTime(raw, "minutes and seconds must be below 60")
	}
	return NewRat(int64(h)*3600*fracScale+int64(m)*60*fracScale+int64(sec)*fracScale+int64(fracV), fracScale), nil
}

// MediaTime converts a tick count at the given timescale (ticks per second)
// into unified media time. A zero timescale is invalid input.
func MediaTime(ticks, timescale int64) (Rat, error) {
	if timescale <= 0 {
		return Rat{}, InvalidInput("timescale must be positive, got %d", timescale)
	}
	return NewRat(ticks, timescale), nil
}

func splitFields(s string, seps []byte) []string {
	out := []string{}
	cur := strings.Builder{}
	for i := 0; i < len(s); i++ {
		if containsByte(seps, s[i]) {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	out = append(out, cur.String())
	return out
}

func containsByte(hay []byte, n byte) bool {
	for _, b := range hay {
		if b == n {
			return true
		}
	}
	return false
}

func parseUintField(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty field")
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid field %q", s)
	}
	return v, nil
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) int64 {
	if v == math.MinInt64 {
		return math.MaxInt64
	}
	if v < 0 {
		return -v
	}
	return v
}
