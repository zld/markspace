// Package media contains the shared time base, error model, immutable source
// records, and content-addressed derivative cache used by all subsystems.
package media

import (
	"errors"
	"fmt"
	"math/big"
)

// Rational is an exact media time base: Num/Den seconds per tick.
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

var (
	// Microsecond is the common presentation time base used by timelines.
	Microsecond = Rational{Num: 1, Den: 1_000_000}
	// Clock90k is the canonical RTP video clock.
	Clock90k = Rational{Num: 1, Den: 90_000}
)

// Validate reports whether the time base is usable.
func (r Rational) Validate() error {
	if r.Num <= 0 || r.Den <= 0 {
		return NewError(CatInvalidArgument, "", -1, -1, "rational time base must be positive: %d/%d", r.Num, r.Den)
	}
	return nil
}

// Convert converts ticks in the from time base to the nearest integer tick in
// the to time base. All arithmetic is exact before rounding.
func Convert(value int64, from, to Rational) (int64, error) {
	if err := from.Validate(); err != nil {
		return 0, err
	}
	if err := to.Validate(); err != nil {
		return 0, err
	}

	n := big.NewInt(value)
	n.Mul(n, big.NewInt(from.Num))
	n.Mul(n, big.NewInt(to.Den))
	d := big.NewInt(from.Den)
	d.Mul(d, big.NewInt(to.Num))

	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(n, d, rem)
	twiceRem := new(big.Int).Mul(rem, big.NewInt(2))
	if twiceRem.CmpAbs(d) >= 0 {
		if n.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(-1))
		}
	}
	if !q.IsInt64() {
		return 0, NewError(CatTimestampAnomaly, "", -1, -1, "converted timestamp overflows int64")
	}
	return q.Int64(), nil
}

// ToMicroseconds is a convenience wrapper around Convert.
func ToMicroseconds(value int64, from Rational) (int64, error) {
	return Convert(value, from, Microsecond)
}

// Category is a stable, machine-readable error category.
type Category string

const (
	CatInvalidArgument   Category = "invalid_argument"
	CatInvalidEncoding   Category = "invalid_encoding"
	CatTruncatedNAL      Category = "truncated_nal"
	CatTruncatedOBU      Category = "truncated_obu"
	CatIllegalLength     Category = "illegal_length"
	CatTimestampAnomaly  Category = "timestamp_anomaly"
	CatDuplicatePacket   Category = "duplicate_packet"
	CatLateWindow        Category = "late_outside_window"
	CatUnrecoverableGap  Category = "unrecoverable_gap"
	CatIntegrityMismatch Category = "integrity_mismatch"
	CatMissingAsset      Category = "missing_asset"
	CatCacheMismatch     Category = "cache_mismatch"
)

// Error is the common error record. Sequence and Offset are -1 when they are
// not meaningful for a particular failure.
type Error struct {
	Category Category `json:"category"`
	TrackID  string   `json:"track_id"`
	Sequence int64    `json:"sequence"`
	Offset   int64    `json:"byte_offset"`
	Message  string   `json:"message"`
}

// NewError constructs a shared media error.
func NewError(category Category, trackID string, sequence, offset int64, format string, args ...any) *Error {
	return &Error{
		Category: category,
		TrackID:  trackID,
		Sequence: sequence,
		Offset:   offset,
		Message:  fmt.Sprintf(format, args...),
	}
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s track=%q seq=%d offset=%d: %s", e.Category, e.TrackID, e.Sequence, e.Offset, e.Message)
}

// Is lets errors.Is match by category sentinel where useful.
func (e *Error) Is(target error) bool {
	var other *Error
	if errors.As(target, &other) {
		return e.Category == other.Category
	}
	return false
}

// Issues is a collection of non-fatal corruption/network evidence.
type Issues []*Error

// Add appends an issue and returns it for convenient use.
func (is *Issues) Add(category Category, trackID string, sequence, offset int64, format string, args ...any) *Error {
	e := NewError(category, trackID, sequence, offset, format, args...)
	*is = append(*is, e)
	return e
}

// Has reports whether an issue with category exists.
func (is Issues) Has(category Category) bool {
	for _, e := range is {
		if e != nil && e.Category == category {
			return true
		}
	}
	return false
}

// CountCategory returns the number of issues in a category.
func (is Issues) CountCategory(category Category) int {
	n := 0
	for _, e := range is {
		if e != nil && e.Category == category {
			n++
		}
	}
	return n
}

// ExitCode maps the shared error model to the command's process contract.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var me *Error
	if errors.As(err, &me) {
		switch me.Category {
		case CatInvalidArgument, CatInvalidEncoding:
			return 2
		}
	}
	return 1
}
