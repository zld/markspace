// Package clock unifies frame-rate and time-code (DF/NDF) conversion onto a
// single rational time base. All downstream modules express intervals using
// the Rat type so that 24/25/29.97/50 fps material can be compared without
// floating-point drift.
package clock

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Rat is a rational number on the unified time base, measured in seconds.
type Rat struct {
	num int64
	den int64 // always positive
}

// NewRat builds a rational and normalizes the denominator to be positive.
func NewRat(num, den int64) (Rat, error) {
	if den == 0 {
		return Rat{}, errors.New("zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := new(big.Int).GCD(nil, nil, big.NewInt(abs64(num)), big.NewInt(den)).Int64()
	return Rat{num: num / g, den: den / g}, nil
}

// MustRat is NewRat for use with constants known to be valid.
func MustRat(num, den int64) Rat {
	r, err := NewRat(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Num returns the numerator.
func (r Rat) Num() int64 { return r.num }

// Den returns the (positive) denominator.
func (r Rat) Den() int64 { return r.den }

// Add returns r+o.
func (r Rat) Add(o Rat) Rat {
	return MustRat(r.num*o.den+o.num*r.den, r.den*o.den)
}

// Sub returns r-o.
func (r Rat) Sub(o Rat) Rat {
	return MustRat(r.num*o.den-o.num*r.den, r.den*o.den)
}

// Mul returns r*n for an integer scalar (frame counts are integers).
func (r Rat) Mul(n int64) Rat {
	return MustRat(r.num*n, r.den)
}

// Cmp returns -1, 0, 1 like strings.Compare.
func (r Rat) Cmp(o Rat) int {
	return new(big.Rat).SetFrac(big.NewInt(r.num), big.NewInt(r.den)).Cmp(
		new(big.Rat).SetFrac(big.NewInt(o.num), big.NewInt(o.den)))
}

// IsNegative reports whether the instant precedes the zero origin.
func (r Rat) IsNegative() bool { return r.num < 0 }

// Float64 returns the value as a float (for reporting only).
func (r Rat) Float64() float64 {
	return float64(r.num) / float64(r.den)
}

// String renders the reduced fraction, e.g. "1001/30000".
func (r Rat) String() string { return fmt.Sprintf("%d/%d", r.num, r.den) }

// Rate describes a declared frame rate and its drop-frame mode.
type Rate struct {
	Num uint32 // frames per second numerator
	Den uint32 // frames per second denominator
	DF  bool   // drop-frame time code
}

// FrameDuration returns the duration of one frame in seconds.
func (r Rate) FrameDuration() Rat {
	return MustRat(int64(r.Den), int64(r.Num))
}

// String renders the canonical rate token, e.g. "30000/1001-df".
func (r Rate) String() string {
	s := fmt.Sprintf("%d/%d", r.Num, r.Den)
	if r.DF {
		return s + "-df"
	}
	return s + "-ndf"
}

// ParseRate accepts the tokens used by the acceptance harness:
//
//	24, 25, 50, 24000/1001, 30000/1001-df, 30000/1001-ndf,
//	29.97-df, 29.97-ndf ...
func ParseRate(tok string) (Rate, error) {
	orig := tok
	df := false
	switch {
	case strings.HasSuffix(tok, "-df"):
		df = true
		tok = tok[:len(tok)-3]
	case strings.HasSuffix(tok, "-ndf"):
		tok = tok[:len(tok)-4]
	}
	var num, den uint32
	switch {
	case strings.Contains(tok, "/"):
		parts := strings.SplitN(tok, "/", 2)
		n, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil {
			return Rate{}, fmt.Errorf("invalid frame rate %q", orig)
		}
		d, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil || d == 0 {
			return Rate{}, fmt.Errorf("invalid frame rate %q", orig)
		}
		num, den = uint32(n), uint32(d)
	case strings.Contains(tok, "."):
		// Only the canonical 29.97 decimal alias is supported.
		f, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			return Rate{}, fmt.Errorf("invalid frame rate %q", orig)
		}
		switch {
		case f > 29.96 && f < 29.98:
			num, den = 30000, 1001
		default:
			return Rate{}, fmt.Errorf("unsupported frame rate %q", orig)
		}
	default:
		n, err := strconv.ParseUint(tok, 10, 32)
		if err != nil || n == 0 {
			return Rate{}, fmt.Errorf("invalid frame rate %q", orig)
		}
		num, den = uint32(n), 1
	}
	if !supported(num, den) {
		return Rate{}, fmt.Errorf("unsupported frame rate %q (only 24, 25, 30000/1001 and 50 fps are declared)", orig)
	}
	if df && !dropCapable(num, den) {
		return Rate{}, fmt.Errorf("drop-frame marker illegal for rate %q", orig)
	}
	return Rate{Num: num, Den: den, DF: df}, nil
}

func supported(num, den uint32) bool {
	switch {
	case den == 1 && (num == 24 || num == 25 || num == 50):
		return true
	case num == 30000 && den == 1001:
		return true
	default:
		return false
	}
}

func dropCapable(num, den uint32) bool { return num == 30000 && den == 1001 }

// Timecode is an HH:MM:SS[:|;]FF label with the rate it was declared against.
type Timecode struct {
	H, M, S, F uint32
	Rate       Rate
}

// ParseTimecode parses SRT/WebVTT style labels and full time codes.
// A ';' separator (or '.' before the frame field) forces drop-frame and must
// match the rate's declared mode.
func ParseTimecode(label string, r Rate) (Timecode, error) {
	tc := Timecode{Rate: r}
	orig := label
	sep := byte(':')
	if i := strings.IndexByte(label, ';'); i >= 0 {
		sep = ';'
		label = label[:i] + ":" + label[i+1:]
	}
	parts := strings.Split(label, ":")
	if len(parts) != 4 {
		return Timecode{}, fmt.Errorf("timecode %q must be HH:MM:SS:FF", orig)
	}
	vals := make([]uint32, 4)
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return Timecode{}, fmt.Errorf("invalid timecode %q", orig)
		}
		vals[i] = uint32(n)
	}
	tc.H, tc.M, tc.S, tc.F = vals[0], vals[1], vals[2], vals[3]
	if tc.M > 59 || tc.S > 59 {
		return Timecode{}, fmt.Errorf("timecode %q field out of range", orig)
	}
	if tc.F >= r.nominalFPS() {
		return Timecode{}, fmt.Errorf("frame %d wraps rate %s", tc.F, r)
	}
	if sep == ';' && !r.DF {
		return Timecode{}, fmt.Errorf("drop-frame separator on NDF rate: %q", orig)
	}
	if r.DF && tc.S == 0 && tc.M%10 != 0 && tc.F < r.droppedPerMinute() {
		return Timecode{}, fmt.Errorf("illegal dropped frame marker in %q", orig)
	}
	return tc, nil
}

func (r Rate) nominalFPS() uint32 {
	// Ceiling of the real frame rate; frame numbers are zero-based below it.
	return (r.Num + r.Den - 1) / r.Den
}

func (r Rate) droppedPerMinute() uint32 {
	// round(fps/15); for 30000/1001 (29.97fps) this yields 2.
	return uint32((int64(r.Num)*2 + int64(r.Den)*15) / (int64(r.Den) * 30))
}

// Frames converts the label to a frame count relative to the zero origin.
// It is the frame-space counterpart of Instant; callers that detect a
// wraparound (frame count below the previous label) report it themselves.
func (tc Timecode) Frames() int64 {
	r := tc.Rate
	total := int64(((tc.H*60+tc.M)*60 + tc.S)) * int64(r.nominalFPS()) + int64(tc.F)
	if r.DF {
		drop := int64(r.droppedPerMinute())
		minutes := int64(tc.H*60 + tc.M)
		total -= drop * (minutes - minutes/10)
	}
	return total
}

// Instant maps a frame count onto the unified rational time base (seconds).
func (r Rate) Instant(frames int64) Rat {
	return MustRat(frames*int64(r.Den), int64(r.Num))
}

// InstantFrom is a convenience combining ParseTimecode and Frames.
func (r Rate) InstantFrom(tc Timecode) Rat {
	return r.Instant(tc.Frames())
}

// NormalizedInterval is an [start,end) interval on the shared time base plus
// the frame-space span, so error reports can carry both representations.
type NormalizedInterval struct {
	Start  Rat
	End    Rat
	Frames int64 // span in source frames
}

// Interval builds the normalized interval between two frame counts.
func (r Rate) Interval(startFrame, endFrame int64) (NormalizedInterval, error) {
	if endFrame < startFrame {
		return NormalizedInterval{}, fmt.Errorf("wraparound: frame %d precedes %d", endFrame, startFrame)
	}
	return NormalizedInterval{
		Start:  r.Instant(startFrame),
		End:    r.Instant(endFrame),
		Frames: endFrame - startFrame,
	}, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
