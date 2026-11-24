// Package model contains the integer nanosecond time basis and typed errors
// shared by the container, timeline, indexing, cutting, and manifest layers.
package model

import (
	"errors"
	"fmt"
	"math"
	"math/big"
)

// NS is the only time representation used for comparisons and alignment.
type NS int64

const (
	Nanosecond  NS = 1
	Microsecond NS = 1000 * Nanosecond
	Millisecond NS = 1000 * Microsecond
	Second      NS = 1000 * Millisecond
	Minute      NS = 60 * Second
)

// Int64 returns the raw integer nanosecond value.
func (t NS) Int64() int64 { return int64(t) }

// Abs returns the absolute duration without floating point arithmetic.
func (t NS) Abs() NS {
	if t < 0 {
		return -t
	}
	return t
}

// Timebase is a positive MP4/MOV mdhd timescale.
type Timebase struct {
	Timescale uint32 `json:"timescale"`
}

// ToNS converts a non-negative raw tick count to integer nanoseconds.
//
// The conversion is floor(raw * 1e9 / timescale). Callers preserve the raw
// tick value separately so no source precision is lost.
func ToNS(raw uint64, timescale uint32) (NS, error) {
	if timescale == 0 {
		return 0, ErrZeroTimescale
	}
	value := new(big.Int).Mul(new(big.Int).SetUint64(raw), big.NewInt(int64(Second)))
	value.Quo(value, big.NewInt(int64(timescale)))
	if !value.IsInt64() {
		return 0, &OverflowError{Raw: raw, Timescale: timescale}
	}
	return NS(value.Int64()), nil
}

// SignedToNS converts an unwrapped signed tick count to integer nanoseconds.
func SignedToNS(raw int64, timescale uint32) (NS, error) {
	if timescale == 0 {
		return 0, ErrZeroTimescale
	}
	if raw == math.MinInt64 {
		return 0, &OverflowError{Raw: uint64(raw), Timescale: timescale}
	}
	sign := int64(1)
	unsigned := raw
	if raw < 0 {
		sign = -1
		unsigned = -raw
	}
	ns, err := ToNS(uint64(unsigned), timescale)
	if err != nil {
		return 0, err
	}
	return NS(sign) * ns, nil
}

// Common typed errors. Exit classification is provided by ExitCode.
var (
	ErrZeroTimescale = errors.New("zero timescale")
	ErrDigestMismatch = errors.New("input digest mismatch")
	ErrCheckpoint    = errors.New("invalid checkpoint")
	ErrAlignment     = errors.New("representation alignment failed")
)

// ParseError identifies a track and byte offset for an invalid container.
type ParseError struct {
	TrackID    int    `json:"track_id"`
	BoxType    string `json:"box_type,omitempty"`
	ByteOffset int64  `json:"byte_offset"`
	Err        error  `json:"-"`
}

func (e *ParseError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("container parse error: track=%d box=%s byte_offset=%d: %v",
		e.TrackID, e.BoxType, e.ByteOffset, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// OverflowError reports a timestamp that does not fit the NS basis.
type OverflowError struct {
	Raw       uint64 `json:"raw"`
	Timescale uint32 `json:"timescale"`
}

func (e *OverflowError) Error() string {
	return fmt.Sprintf("timestamp conversion overflow: raw=%d timescale=%d", e.Raw, e.Timescale)
}

// ConfigError indicates an invalid command or processing configuration.
type ConfigError struct {
	Field string
	Value any
	Err   error
}

func (e *ConfigError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("invalid configuration %s=%v: %v", e.Field, e.Value, e.Err)
	}
	return fmt.Sprintf("invalid configuration %s=%v", e.Field, e.Value)
}

func (e *ConfigError) Unwrap() error { return e.Err }

// IntegrityError indicates corrupted input or a failed reproducibility check.
type IntegrityError struct {
	Path string
	Err  error
}

func (e *IntegrityError) Error() string {
	return fmt.Sprintf("integrity check failed for %s: %v", e.Path, e.Err)
}

func (e *IntegrityError) Unwrap() error { return e.Err }

// AlignmentError reports a representation boundary outside tolerance.
type AlignmentError struct {
	Representation string `json:"representation"`
	TrackID        uint32 `json:"track_id"`
	Boundary       string `json:"boundary"`
	DeviationNS    NS     `json:"deviation_ns"`
	ToleranceNS    NS     `json:"tolerance_ns"`
}

func (e *AlignmentError) Error() string {
	return fmt.Sprintf("%s track=%d %s boundary deviation=%dns exceeds tolerance=%dns",
		e.Representation, e.TrackID, e.Boundary, e.DeviationNS, e.ToleranceNS)
}

func (e *AlignmentError) Unwrap() error { return ErrAlignment }

// ExitCode maps expected failures to the acceptance contract:
// 0 success, 2 invalid input/integrity, 3 invalid arguments/config.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var cfg *ConfigError
	if errors.As(err, &cfg) {
		return 3
	}
	var parse *ParseError
	if errors.As(err, &parse) {
		return 2
	}
	var integrity *IntegrityError
	if errors.As(err, &integrity) {
		return 2
	}
	if errors.Is(err, ErrDigestMismatch) || errors.Is(err, ErrAlignment) {
		return 2
	}
	return 1
}

// ByteOffset extracts a parse error offset when one is available.
func ByteOffset(err error) (int64, bool) {
	var parse *ParseError
	if errors.As(err, &parse) && parse != nil {
		return parse.ByteOffset, true
	}
	return -1, false
}
