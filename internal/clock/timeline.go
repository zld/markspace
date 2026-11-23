// Package clock maintains the unified integer time base. Every DASH timescale
// and every HLS playlist duration is mapped onto master ticks; all arithmetic
// is exact (no floating point) and every lossy or overflowing conversion is
// rejected instead of rounded.
package clock

import (
	"math/big"
	"strings"

	"mediacheck/internal/model"
)

// MasterTicksPerSecond is the common tick rate. 90 kHz divides the RTP video
// clock and the common media timescales (24000/44100/48000 handled as exact
// rational maps) without requiring float conversion.
const MasterTicksPerSecond int64 = 90000

var (
	bigMaster = big.NewInt(MasterTicksPerSecond)
	bigMax    = big.NewInt(0).SetInt64(1 << 62)
)

// ToMaster converts value expressed in timescale units into master ticks.
// It returns model.ErrZeroTimescale for a non-positive timescale,
// model.ErrOverflow when the result does not fit in int64 and
// model.ErrLossyTime when no exact integer representation exists.
func ToMaster(value, timescale int64) (int64, error) {
	if timescale <= 0 {
		return 0, model.ErrZeroTimescale
	}
	if value < 0 {
		return 0, model.ErrLossyTime
	}
	num := big.NewInt(value)
	num.Mul(num, bigMaster)
	den := big.NewInt(timescale)
	q := new(big.Int)
	r := new(big.Int)
	q.QuoRem(num, den, r)
	if r.Sign() != 0 {
		return 0, model.ErrLossyTime
	}
	if q.Cmp(bigMax) >= 0 || q.Sign() < 0 {
		return 0, model.ErrOverflow
	}
	return q.Int64(), nil
}

// FromSeconds converts an HLS decimal duration such as "6.0" or "0.96" into
// exact master ticks. Floating point parsing is deliberately avoided so that
// playlist timelines are reproducible byte for byte.
func FromSeconds(decimal string) (int64, error) {
	s := strings.TrimSpace(decimal)
	if s == "" || strings.HasSuffix(s, ".") {
		return 0, model.ErrLossyTime
	}
	whole, frac := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		whole, frac = s[:dot], s[dot+1:]
	}
	if whole == "" {
		whole = "0"
	}
	wholeN, ok := parseUint(whole)
	if !ok {
		return 0, model.ErrLossyTime
	}
	// At most 9 fractional digits: 1e-9 s is below one master tick anyway.
	if len(frac) > 9 {
		return 0, model.ErrLossyTime
	}
	denom := int64(1)
	fracN := int64(0)
	for _, c := range frac {
		if c < '0' || c > '9' {
			return 0, model.ErrLossyTime
		}
		fracN = fracN*10 + int64(c-'0')
		denom *= 10
	}
	num := big.NewInt(wholeN)
	num.Mul(num, bigMaster)
	if fracN != 0 {
		num.Add(num, big.NewInt(fracN*MasterTicksPerSecond/denom))
		if (fracN*MasterTicksPerSecond)%denom != 0 {
			return 0, model.ErrLossyTime
		}
	}
	if num.Cmp(bigMax) >= 0 {
		return 0, model.ErrOverflow
	}
	return num.Int64(), nil
}

func parseUint(s string) (int64, bool) {
	var n int64
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
		if n < 0 {
			return 0, false
		}
	}
	return n, true
}

// Timeline is a monotonic cursor over master ticks. Discontinuity boundaries
// are recorded so reports can explain timestamp resets instead of treating
// them as backwards motion.
type Timeline struct {
	cursor          int64
	discontinuities []model.Range
}

// NewTimeline starts at tick zero.
func NewTimeline() *Timeline { return &Timeline{} }

// Cursor returns the current master-tick position.
func (t *Timeline) Cursor() int64 { return t.cursor }

// Advance moves the cursor forward and rejects backwards motion.
func (t *Timeline) Advance(ticks int64) (int64, error) {
	if ticks < 0 {
		return t.cursor, model.ErrLossyTime
	}
	next := t.cursor + ticks
	if next < t.cursor {
		return t.cursor, model.ErrOverflow
	}
	start := t.cursor
	t.cursor = next
	return start, nil
}

// MarkDiscontinuity records a reset boundary. HLS #EXT-X-DISCONTINUITY means
// the segment-level timestamps restart; on the unified timeline this is simply
// a new contiguous region, and the boundary interval is kept for reporting.
func (t *Timeline) MarkDiscontinuity() model.Range {
	r := model.Range{Start: t.cursor, End: t.cursor + 1}
	t.discontinuities = append(t.discontinuities, r)
	return r
}

// Discontinuities returns the recorded reset boundaries in order.
func (t *Timeline) Discontinuities() []model.Range {
	out := make([]model.Range, len(t.discontinuities))
	copy(out, t.discontinuities)
	return out
}
