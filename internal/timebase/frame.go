// Package timebase maps media time onto integer frame numbers using rational
// frame rates and exact integer arithmetic.
//
// Cues are treated as half-open intervals [start, end): the start is rounded
// down to a frame boundary and the end is rounded up, so no displayed frame is
// ever lost to rounding.
package timebase

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Rate is a rational frame rate in frames per second.
type Rate struct {
	Num int64 // numerator
	Den int64 // denominator
}

// ParseRate accepts "24000/1001" or a bare integer such as "25".
func ParseRate(s string) (Rate, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rate{}, errors.New("empty frame rate")
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		num, err := strconv.ParseInt(strings.TrimSpace(s[:i]), 10, 64)
		if err != nil {
			return Rate{}, fmt.Errorf("frame rate numerator: %w", err)
		}
		den, err := strconv.ParseInt(strings.TrimSpace(s[i+1:]), 10, 64)
		if err != nil {
			return Rate{}, fmt.Errorf("frame rate denominator: %w", err)
		}
		if num <= 0 || den <= 0 {
			return Rate{}, errors.New("frame rate must be positive")
		}
		return Rate{Num: num, Den: den}, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return Rate{}, fmt.Errorf("invalid frame rate %q", s)
	}
	return Rate{Num: n, Den: 1}, nil
}

// Frames is a half-open frame interval [Start, End).
type Frames struct {
	Start int64
	End   int64
}

// Length is the number of frames the interval covers.
func (f Frames) Length() int64 { return f.End - f.Start }

var (
	bigFramePeriodNS = big.NewInt(int64(time.Second))
	bigMilli         = big.NewInt(int64(time.Millisecond))
	bigCenti         = big.NewInt(10 * int64(time.Millisecond))
	maxInt64         = big.NewInt(1<<63 - 1)
)

// Map converts a time interval into a frame interval. It rejects non-positive
// durations and values that overflow int64 frame numbers.
func (r Rate) Map(start, end time.Duration) (Frames, error) {
	if r.Num <= 0 || r.Den <= 0 {
		return Frames{}, errors.New("invalid frame rate")
	}
	if end <= start {
		return Frames{}, fmt.Errorf("non-positive cue duration: start=%s end=%s", start, end)
	}
	// denominator in nanoseconds per second-denominator unit
	denNS := new(big.Int).Mul(big.NewInt(r.Den), bigFramePeriodNS)
	startFrame := big.NewInt(0).Mul(big.NewInt(start.Nanoseconds()), big.NewInt(r.Num))
	startFrame.Quo(startFrame, denNS) // floor: inputs are non-negative
	numEnd := big.NewInt(0).Mul(big.NewInt(end.Nanoseconds()), big.NewInt(r.Num))
	// ceil(a/b) = (a + b - 1) / b
	endFrame := big.NewInt(0).Add(numEnd, big.NewInt(0).Sub(denNS, big.NewInt(1)))
	endFrame.Quo(endFrame, denNS)
	if startFrame.Cmp(maxInt64) > 0 || endFrame.Cmp(maxInt64) > 0 {
		return Frames{}, fmt.Errorf("frame number overflow at %s", end)
	}
	sf, ef := startFrame.Int64(), endFrame.Int64()
	if ef <= sf {
		return Frames{}, fmt.Errorf("cue shorter than one frame: %s", end-start)
	}
	return Frames{Start: sf, End: ef}, nil
}

// FrameTime returns the exact media time at which frame f begins.
func (r Rate) FrameTime(f int64) time.Duration {
	num := new(big.Int).Mul(big.NewInt(f), big.NewInt(r.Den))
	num.Mul(num, bigFramePeriodNS)
	ns := num.Quo(num, big.NewInt(r.Num))
	return time.Duration(ns.Int64())
}

// RoundMillis rounds a duration to whole milliseconds (half away from zero).
func RoundMillis(d time.Duration) int64 {
	return scaledRounded(d.Nanoseconds(), int64(time.Millisecond))
}

// RoundCentis rounds a duration to whole hundredths of a second.
func RoundCentis(d time.Duration) int64 {
	return scaledRounded(d.Nanoseconds(), 10*int64(time.Millisecond))
}

func scaledRounded(ns, scale int64) int64 {
	if ns >= 0 {
		return (ns + scale/2) / scale
	}
	return -((-ns + scale/2) / scale)
}

// MillisDuration converts millisecond offsets back to a duration.
func MillisDuration(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// CentisDuration converts centisecond offsets back to a duration.
func CentisDuration(cs int64) time.Duration {
	return time.Duration(cs) * 10 * time.Millisecond
}
