// Package timeline defines integer media time bases and overflow-checked
// conversions between them. All time arithmetic stays in integer ticks; a
// conversion that would overflow an int64 is reported through mediaerr.
package timeline

import (
	"math"
	"math/big"

	mediaerr "mediacheck/internal/errors"
)

// TimeBase is an exact rational scale: one tick equals Num/Den seconds.
// Both components are stored explicitly so reports can reconstruct the
// original index units without floating point drift.
type TimeBase struct {
	Num uint64 `json:"num"`
	Den uint64 `json:"den"`
}

// Valid reports whether the time base is usable (non-zero denominator and
// components that fit in int64, so all later arithmetic can stay signed).
func (tb TimeBase) Valid() bool {
	return tb.Num > 0 && tb.Den > 0 && tb.Num <= math.MaxInt64 && tb.Den <= math.MaxInt64
}

// Check returns a unified error when the time base cannot be used.
func (tb TimeBase) Check(track string) error {
	if !tb.Valid() {
		return mediaerr.New(mediaerr.StageTimebase, track,
			"invalid time base num=%d den=%d", tb.Num, tb.Den)
	}
	return nil
}

// Rescale converts ticks from src into dst tick units without precision loss.
// It returns an overflow error instead of a wrapped value when the result
// cannot be represented in int64.
func Rescale(ticks int64, src, dst TimeBase) (int64, error) {
	if err := src.Check(""); err != nil {
		return 0, err
	}
	if err := dst.Check(""); err != nil {
		return 0, err
	}
	if ticks == 0 {
		return 0, nil
	}
	// dstTicks = ticks * src.Num * dst.Den / (src.Den * dst.Num)
	num := new(big.Int).Mul(big.NewInt(ticks), big.NewInt(int64(src.Num)))
	num.Mul(num, big.NewInt(int64(dst.Den)))
	den := new(big.Int).Mul(big.NewInt(int64(src.Den)), big.NewInt(int64(dst.Num)))
	res := num.Quo(num, den)
	if !res.IsInt64() {
		return 0, mediaerr.New(mediaerr.StageTimebase, "",
			"rescale overflow: ticks=%d %d/%d -> %d/%d",
			ticks, src.Num, src.Den, dst.Num, dst.Den)
	}
	return res.Int64(), nil
}

// Stamp is a timestamp expressed in integer ticks of a known time base.
// OriginalTS preserves the value as delivered by the external keyframe
// index so reconciliation can report offsets in index units.
type Stamp struct {
	TS         int64    `json:"ts"`
	Base       TimeBase `json:"base"`
	OriginalTS int64    `json:"original_ts"`
}

// NewStamp creates a stamp whose original index value equals ticks.
func NewStamp(ticks int64, base TimeBase) Stamp {
	return Stamp{TS: ticks, Base: base, OriginalTS: ticks}
}

// In converts the stamp into another time base, keeping OriginalTS intact.
func (s Stamp) In(dst TimeBase) (Stamp, error) {
	t, err := Rescale(s.TS, s.Base, dst)
	if err != nil {
		return Stamp{}, err
	}
	return Stamp{TS: t, Base: dst, OriginalTS: s.OriginalTS}, nil
}

// Add returns s+d in the same time base, guarding against int64 overflow.
func (s Stamp) Add(delta int64) (Stamp, error) {
	r, ok := addOverflow(s.TS, delta)
	if !ok {
		return Stamp{}, mediaerr.New(mediaerr.StageTimebase, "",
			"timestamp addition overflow: %d+%d", s.TS, delta)
	}
	return Stamp{TS: r, Base: s.Base, OriginalTS: s.OriginalTS}, nil
}

func addOverflow(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}
