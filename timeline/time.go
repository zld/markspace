package timeline

import (
	"fmt"
	"math"
	"math/big"

	"mediapack/model"
)

// NanosPerSecond is the project-wide common timeline resolution.
const NanosPerSecond int64 = 1_000_000_000

// Timebase is the rational duration of one timestamp tick:
// one tick lasts Numerator/Denominator seconds.
type Timebase struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

// CommonTimebase returns the nanosecond timebase.
func CommonTimebase() Timebase {
	return Timebase{Numerator: 1, Denominator: NanosPerSecond}
}

// RTP90kTimebase is the conventional RTP video timebase.
func RTP90kTimebase() Timebase {
	return Timebase{Numerator: 1, Denominator: 90_000}
}

// MillisecondTimebase is useful for ID3/emsg fixtures.
func MillisecondTimebase() Timebase {
	return Timebase{Numerator: 1, Denominator: 1_000}
}

// Validate rejects a zero or negative rational timebase.
func (tb Timebase) Validate() error {
	if tb.Numerator <= 0 || tb.Denominator <= 0 {
		return fmt.Errorf("invalid timebase %d/%d", tb.Numerator, tb.Denominator)
	}
	return nil
}

// ToNanos converts signed ticks to the common nanosecond timeline using
// nearest-integer rounding. Overflow is reported rather than wrapped.
func (tb Timebase) ToNanos(ticks int64) (int64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}
	num := new(big.Int).Mul(big.NewInt(ticks), big.NewInt(tb.Numerator))
	num.Mul(num, big.NewInt(NanosPerSecond))
	den := big.NewInt(tb.Denominator)

	// Round to nearest, with halves rounded away from zero.
	absNum := new(big.Int).Abs(num)
	q, r := new(big.Int).QuoRem(absNum, den, new(big.Int))
	twoR := new(big.Int).Lsh(r, 1)
	if twoR.Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if num.Sign() < 0 {
		q.Neg(q)
	}
	if !q.IsInt64() {
		return 0, fmt.Errorf("converted timestamp overflows int64: ticks=%d tb=%d/%d", ticks, tb.Numerator, tb.Denominator)
	}
	return q.Int64(), nil
}

// FromNanos converts common-timeline nanoseconds to this timebase, nearest.
func (tb Timebase) FromNanos(nanos int64) (int64, error) {
	if err := tb.Validate(); err != nil {
		return 0, err
	}
	// ticks = nanos * den / (num * 1e9)
	num := new(big.Int).Mul(big.NewInt(nanos), big.NewInt(tb.Denominator))
	den := new(big.Int).Mul(big.NewInt(tb.Numerator), big.NewInt(NanosPerSecond))
	absNum := new(big.Int).Abs(num)
	q, r := new(big.Int).QuoRem(absNum, den, new(big.Int))
	if twice := new(big.Int).Lsh(r, 1); twice.Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if num.Sign() < 0 {
		q.Neg(q)
	}
	if !q.IsInt64() {
		return 0, fmt.Errorf("timestamp in timebase overflows int64")
	}
	return q.Int64(), nil
}

// Timestamp is a source timestamp plus the information required to audit it.
type Timestamp struct {
	Track    model.TrackID
	Source   string
	Raw      uint64
	Timebase Timebase
	Bits     uint
}

// Observation is the unwrapped, common-timeline result for one raw timestamp.
type Observation struct {
	Raw        uint64 `json:"raw"`
	Extended   int64  `json:"extended_ticks"`
	Nanos      int64  `json:"nanos"`
	Duplicate  bool   `json:"duplicate"`
	OutOfOrder bool   `json:"out_of_order"`
	Wrapped    bool   `json:"wrapped"`
}

// Unwrapper extends fixed-width timestamp counters and identifies ambiguous
// observations. It is not safe for concurrent use; each track owns one.
type Unwrapper struct {
	track        model.TrackID
	timebase     Timebase
	bits         uint
	span         uint64
	mask         uint64
	maxReorder   int64
	initialized  bool
	lastRaw      uint64
	lastExtended int64
}

// NewUnwrapper creates an unwrapper. bits=0 means a full uint64 timestamp with
// no wraparound; maxReorderTicks bounds acceptable backward arrival displacement.
func NewUnwrapper(track model.TrackID, tb Timebase, bits uint, maxReorderTicks int64) (*Unwrapper, error) {
	if err := tb.Validate(); err != nil {
		return nil, model.NewError(track, "", -1, model.CategoryInvalidArgument, "invalid timebase", err)
	}
	if bits > 64 {
		return nil, model.NewError(track, "", -1, model.CategoryInvalidArgument, "timestamp width > 64 bits", nil)
	}
	if maxReorderTicks < 0 {
		return nil, model.NewError(track, "", -1, model.CategoryInvalidArgument, "max reorder must be non-negative", nil)
	}
	u := &Unwrapper{track: track, timebase: tb, bits: bits, maxReorder: maxReorderTicks}
	if bits == 0 || bits == 64 {
		u.span = 0
		u.mask = math.MaxUint64
	} else {
		u.span = uint64(1) << bits
		u.mask = u.span - 1
	}
	return u, nil
}

// Observe extends raw and converts it to nanoseconds. Duplicate raw values are
// returned once as duplicates. A displacement that can be either reordering or
// wraparound is an AmbiguousTimestamp error containing track and source.
func (u *Unwrapper) Observe(raw uint64, source string) (Observation, error) {
	if u.bits > 0 && u.bits < 64 && raw > u.mask {
		return Observation{}, model.NewError(u.track, source, -1,
			model.CategoryAmbiguousTimestamp,
			fmt.Sprintf("raw timestamp %d exceeds %d-bit range", raw, u.bits), nil)
	}
	if u.bits == 64 && raw > math.MaxInt64 {
		return Observation{}, model.NewError(u.track, source, -1,
			model.CategoryAmbiguousTimestamp, "uint64 timestamp exceeds signed extension range", nil)
	}
	raw &= u.mask
	if !u.initialized {
		u.initialized = true
		u.lastRaw = raw
		u.lastExtended = int64(raw)
		nanos, err := u.timebase.ToNanos(u.lastExtended)
		if err != nil {
			return Observation{}, model.NewError(u.track, source, -1, model.CategoryAmbiguousTimestamp, "conversion failed", err)
		}
		return Observation{Raw: raw, Extended: u.lastExtended, Nanos: nanos}, nil
	}

	if raw == u.lastRaw {
		nanos, err := u.timebase.ToNanos(u.lastExtended)
		if err != nil {
			return Observation{}, model.NewError(u.track, source, -1, model.CategoryAmbiguousTimestamp, "conversion failed", err)
		}
		return Observation{Raw: raw, Extended: u.lastExtended, Nanos: nanos, Duplicate: true}, nil
	}

	extended := u.lastExtended
	var delta int64
	outOfOrder := false
	wrapped := false

	if u.span == 0 {
		// Full-width counter: only direct signed arithmetic is meaningful.
		delta = int64(raw - u.lastRaw)
		if delta < 0 {
			outOfOrder = true
		}
		extended += delta
	} else {
		forward := (raw - u.lastRaw + u.span) % u.span
		half := u.span / 2
		switch {
		case forward == half:
			return Observation{}, model.NewError(u.track, source, -1, model.CategoryAmbiguousTimestamp,
				fmt.Sprintf("raw=%d is exactly half wraparound span from last=%d", raw, u.lastRaw), nil)
		case forward < half:
			delta = int64(forward)
			if u.lastExtended > math.MaxInt64-delta {
				return Observation{}, model.NewError(u.track, source, -1, model.CategoryAmbiguousTimestamp, "wrapped extension overflows", nil)
			}
			extended += delta
			// raw decreased while the modular forward distance is short,
			// so the extension crossed the counter wrap point.
			if raw < u.lastRaw {
				wrapped = true
			}
		default:
			backward := int64(u.span - forward)
			if backward > u.maxReorder {
				return Observation{}, model.NewError(u.track, source, -1, model.CategoryAmbiguousTimestamp,
					fmt.Sprintf("raw=%d after last=%d can be %d-tick reorder or %d-tick wrap; max_reorder=%d",
						raw, u.lastRaw, backward, forward, u.maxReorder), nil)
			}
			delta = -backward
			outOfOrder = true
			extended -= backward
		}
	}

	nanos, err := u.timebase.ToNanos(extended)
	if err != nil {
		return Observation{}, model.NewError(u.track, source, -1, model.CategoryAmbiguousTimestamp, "conversion failed", err)
	}
	u.lastRaw = raw
	u.lastExtended = extended
	return Observation{Raw: raw, Extended: extended, Nanos: nanos, OutOfOrder: outOfOrder, Wrapped: wrapped}, nil
}
