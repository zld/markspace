package model

import (
	"errors"
	"fmt"
	"math/big"
)

// TimeBase is the rational definition of one tick: Num/Den seconds.
// Den must be positive. A zero value is not a valid time base.
type TimeBase struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

func (b TimeBase) Validate() error {
	if b.Num <= 0 || b.Den <= 0 {
		return fmt.Errorf("invalid time base %d/%d", b.Num, b.Den)
	}
	return nil
}

func (b TimeBase) SecondsPerTick() float64 {
	return float64(b.Num) / float64(b.Den)
}

// Millis returns the exact integer millisecond representation, rounding to
// nearest. It is intended for reports and thresholds, not muxing decisions.
func (b TimeBase) Millis(ticks int64) int64 {
	if b.Num == 0 || b.Den == 0 {
		return 0
	}
	num := big.NewInt(ticks)
	num.Mul(num, big.NewInt(b.Num))
	num.Mul(num, big.NewInt(1000))
	den := big.NewInt(b.Den)
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if absInt(r.Int64()*2) >= b.Den {
		if q.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(-1))
		}
	}
	return q.Int64()
}

func absInt(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Timestamp is an integer tick count in an explicit rational time base.
type Timestamp struct {
	Ticks int64    `json:"ticks"`
	Base  TimeBase `json:"base"`
}

func NewTimestamp(ticks int64, base TimeBase) Timestamp {
	return Timestamp{Ticks: ticks, Base: base}
}

func Milliseconds(ms int64) Timestamp {
	return Timestamp{Ticks: ms, Base: TimeBase{Num: 1, Den: 1000}}
}

func (t Timestamp) Millis() int64 { return t.Base.Millis(t.Ticks) }

// Convert returns the same instant in dst, using exact rational arithmetic and
// round-to-nearest tick. Callers that need lossless equality should compare
// rational instants with Compare rather than converted tick counts.
func (t Timestamp) Convert(dst TimeBase) Timestamp {
	if err := dst.Validate(); err != nil {
		panic(err)
	}
	// ticks * Num/Den * dst.Den/dst.Num
	n := big.NewInt(t.Ticks)
	n.Mul(n, big.NewInt(t.Base.Num))
	n.Mul(n, big.NewInt(dst.Den))
	d := big.NewInt(t.Base.Den)
	d.Mul(d, big.NewInt(dst.Num))
	q, r := new(big.Int).QuoRem(n, d, new(big.Int))
	twoR := new(big.Int).Mul(r, big.NewInt(2))
	if twoR.Cmp(d) >= 0 {
		if q.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	return Timestamp{Ticks: q.Int64(), Base: dst}
}

// Compare compares rational instants without converting either time base.
func (t Timestamp) Compare(o Timestamp) int {
	l := new(big.Int).Mul(big.NewInt(t.Ticks), big.NewInt(t.Base.Num))
	l.Mul(l, big.NewInt(o.Base.Den))
	r := new(big.Int).Mul(big.NewInt(o.Ticks), big.NewInt(o.Base.Num))
	r.Mul(r, big.NewInt(t.Base.Den))
	return l.Cmp(r)
}

func (t Timestamp) Before(o Timestamp) bool { return t.Compare(o) < 0 }
func (t Timestamp) After(o Timestamp) bool  { return t.Compare(o) > 0 }

func (t Timestamp) Add(d Timestamp) Timestamp {
	dTicks := d.Convert(t.Base).Ticks
	return Timestamp{Ticks: t.Ticks + dTicks, Base: t.Base}
}

var ErrInvalidTimeBase = errors.New("invalid time base")
