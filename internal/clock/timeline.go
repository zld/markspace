// Package clock provides the single media time base shared by the frame
// pipeline, the RTP audio path and the MP4 muxer.
//
// All times are integer ticks at a declared timescale, which keeps results
// deterministic across runs. RTP timestamps are 32 bit and wrap; Unwrapper
// extends them to a monotonic clock before they enter the timeline.
package clock

import (
	"errors"
	"fmt"
	"math/big"
	"math/bits"
)

// ErrWrapAmbiguous is returned when a timestamp jump cannot be resolved.
var ErrWrapAmbiguous = errors.New("rtp timestamp jump exceeds half range")

// Rational is a positive rational number Num/Den.
type Rational struct {
	Num int64
	Den int64
}

// NewRational validates and returns a Rational.
func NewRational(num, den int64) (Rational, error) {
	if num <= 0 || den <= 0 {
		return Rational{}, fmt.Errorf("rational %d/%d must be positive", num, den)
	}
	return Rational{Num: num, Den: den}, nil
}

// FPS returns the rational frames-per-second value.
func (r Rational) FPS() float64 { return float64(r.Num) / float64(r.Den) }

func (r Rational) valid() bool { return r.Num > 0 && r.Den > 0 }

// Timeline is an immutable tick domain. Origin is the raw media tick that is
// presented as local tick zero.
type Timeline struct {
	Timescale uint32
	Origin    int64
}

// NewTimeline creates a tick domain. A zero timescale is rejected.
func NewTimeline(timescale uint32) (*Timeline, error) {
	if timescale == 0 {
		return nil, fmt.Errorf("timeline timescale must be non-zero")
	}
	return &Timeline{Timescale: timescale}, nil
}

// TickAtNS converts a nanosecond instant to ticks, rounding down.
func (t *Timeline) TickAtNS(ns int64) int64 {
	return mulDiv(ns, int64(t.Timescale), 1_000_000_000)
}

// NSAtTick converts ticks to nanoseconds, rounding down.
func (t *Timeline) NSAtTick(tick int64) int64 {
	return mulDiv(tick, 1_000_000_000, int64(t.Timescale))
}

// Local converts a raw media tick into the timeline domain.
func (t *Timeline) Local(raw int64) int64 { return raw - t.Origin }

// Rebucket converts local ticks into another timescale, rounding to nearest.
func (t *Timeline) Rebucket(local int64, dstTimescale uint32) int64 {
	if dstTimescale == 0 {
		return 0
	}
	return mulDivRound(local, int64(dstTimescale), int64(t.Timescale))
}

// Transition marks where the nominal frame rate changes.
type Transition struct {
	StartFrame int
	FPS        Rational
}

// RateSchedule maps frame indices to local ticks for content whose frame rate
// changes part way through (variable frame rate deliveries).
type RateSchedule struct {
	Timeline    *Timeline
	transitions []Transition
}

// NewRateSchedule validates the first transition begins at frame zero.
func NewRateSchedule(tl *Timeline, fps Rational) (*RateSchedule, error) {
	if tl == nil {
		return nil, fmt.Errorf("rate schedule needs a timeline")
	}
	if !fps.valid() {
		return nil, fmt.Errorf("frame rate must be positive")
	}
	return &RateSchedule{Timeline: tl, transitions: []Transition{{StartFrame: 0, FPS: fps}}}, nil
}

// AddTransition appends a rate change. Frames must strictly advance.
func (s *RateSchedule) AddTransition(startFrame int, fps Rational) error {
	if !fps.valid() {
		return fmt.Errorf("frame rate must be positive")
	}
	last := s.transitions[len(s.transitions)-1]
	if startFrame <= last.StartFrame {
		return fmt.Errorf("transition at frame %d must follow frame %d", startFrame, last.StartFrame)
	}
	s.transitions = append(s.transitions, Transition{StartFrame: startFrame, FPS: fps})
	return nil
}

// TickAtFrame returns the local tick at the start of a frame index. The whole
// preceding timeline is accumulated as an exact rational and rounded once, so
// long variable rate sequences do not drift.
func (s *RateSchedule) TickAtFrame(frame int) int64 {
	if frame < 0 {
		frame = 0
	}
	// total seconds = sum over transitions of (frames in segment / fps)
	total := new(big.Rat)
	prev := 0
	for i, tr := range s.transitions {
		end := frame
		if i+1 < len(s.transitions) && s.transitions[i+1].StartFrame < end {
			end = s.transitions[i+1].StartFrame
		}
		if end > prev {
			seg := big.NewRat(int64(end-prev), 1)
			rate := big.NewRat(tr.FPS.Num, tr.FPS.Den)
			total.Add(total, new(big.Rat).Quo(seg, rate))
			prev = end
		}
		if end == frame {
			break
		}
	}
	ticks := new(big.Rat).Mul(total, big.NewRat(int64(s.Timeline.Timescale), 1))
	return ratRound(ticks)
}

// FrameDuration returns the nominal local tick span of the rate at frame.
func (s *RateSchedule) FrameDuration(frame int) int64 {
	fps := s.transitions[0].FPS
	for _, tr := range s.transitions {
		if tr.StartFrame <= frame {
			fps = tr.FPS
		}
	}
	return mulDivRound(fps.Den, int64(s.Timeline.Timescale), fps.Num)
}

// WithinTolerance reports whether |a-b| is at most tol ticks.
func WithinTolerance(a, b, tol int64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// mulDiv computes floor(a*b/c) exactly using a 128 bit intermediate.
func mulDiv(a, b, c int64) int64 {
	q, _ := mulDivRem(a, b, c)
	return q
}

// mulDivRound computes round-half-away-from-zero(a*b/c) exactly.
func mulDivRound(a, b, c int64) int64 {
	q, r := mulDivRem(a, b, c)
	if c == 0 {
		return 0
	}
	cAbs := c
	if cAbs < 0 {
		cAbs = -cAbs
	}
	if r < 0 {
		r = -r
	}
	if r*2 >= cAbs {
		if (a < 0) != (b < 0) {
			q--
		} else {
			q++
		}
	}
	return q
}

// mulDivRem splits the computation into 64 bit halves so the numerator never
// overflows. Quotient is truncated toward zero; remainder follows the sign of
// the product.
func mulDivRem(a, b, c int64) (int64, int64) {
	if c == 0 {
		return 0, 0
	}
	neg := (a < 0) != (b < 0)
	ua, ub, uc := abs64(a), abs64(b), abs64(c)
	hi, lo := bits.Mul64(ua, ub)
	if hi >= uc {
		// Quotient does not fit in 64 bits; saturate rather than panic.
		return 0, 0
	}
	q, r := bits.Div64(hi, lo, uc)
	if neg {
		return -int64(q), -int64(r)
	}
	return int64(q), int64(r)
}

func abs64(v int64) uint64 {
	if v < 0 {
		return uint64(-v)
	}
	return uint64(v)
}

func ratRound(r *big.Rat) int64 {
	num, den := r.Num(), r.Denom()
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if new(big.Int).Lsh(new(big.Int).Abs(rem), 1).Cmp(den) >= 0 {
		if r.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q.Int64()
}

func addChecked(a, b int64) (int64, bool) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, false
	}
	return s, true
}

// ScaleTicks converts a duration between two tick domains, rounding to nearest.
func ScaleTicks(value int64, from, to uint32) int64 {
	if from == 0 || to == 0 {
		return 0
	}
	return mulDivRound(value, int64(to), int64(from))
}
