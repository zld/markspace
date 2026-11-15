// Package timeline performs exact frame-rate conversion. All math uses
// integer rational arithmetic so no floating-point rounding can move a cue
// onto the wrong frame.
package timeline

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FPS is a frame rate expressed as a positive integer fraction, e.g.
// 24000/1001 for 23.976 fps.
type FPS struct {
	Num int64
	Den int64
}

// ParseFPS accepts "24000/1001" or a bare positive integer such as "25".
func ParseFPS(s string) (FPS, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return FPS{}, fmt.Errorf("empty frame rate")
	}
	numS, denS, hasSlash := s, "1", false
	if i := strings.IndexByte(s, '/'); i >= 0 {
		numS, denS, hasSlash = s[:i], s[i+1:], true
	}
	num, err := strconv.ParseInt(strings.TrimSpace(numS), 10, 64)
	if err != nil {
		return FPS{}, fmt.Errorf("invalid frame rate numerator %q", numS)
	}
	den, err := strconv.ParseInt(strings.TrimSpace(denS), 10, 64)
	if err != nil {
		return FPS{}, fmt.Errorf("invalid frame rate denominator %q", denS)
	}
	if num <= 0 || den <= 0 {
		return FPS{}, fmt.Errorf("frame rate must use positive integers, got %s", s)
	}
	if !hasSlash {
		den = 1
	}
	return FPS{Num: num, Den: den}, nil
}

// String renders the canonical "num/den" form, collapsing "/1".
func (f FPS) String() string {
	if f.Den == 1 {
		return strconv.FormatInt(f.Num, 10)
	}
	return fmt.Sprintf("%d/%d", f.Num, f.Den)
}

// FrameRange maps a half-open time span onto inclusive frame numbers.
// The start frame is floored, the end frame is ceiled, and any cue shorter
// than one frame still occupies at least a single frame.
//
//	frame(time) = time_ns * fps.Num / (1e9 * fps.Den)
func (f FPS) FrameRange(start, end time.Duration) (startFrame, endFrame int64) {
	const nsPerSec int64 = int64(time.Second)
	base := nsPerSec * f.Den
	sn, en := int64(start), int64(end)

	startFrame = floorDiv(sn*f.Num, base)
	endFrame = ceilDiv(en*f.Num, base)
	if endFrame <= startFrame {
		endFrame = startFrame + 1
	}
	return startFrame, endFrame
}

// FrameTime converts a frame number back to its exact nanosecond offset.
func (f FPS) FrameTime(frame int64) time.Duration {
	const nsPerSec int64 = int64(time.Second)
	return time.Duration(divFloor(frame*nsPerSec*f.Den, f.Num))
}

func floorDiv(a, b int64) int64 {
	q, r := a/b, a%b
	if r != 0 && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func ceilDiv(a, b int64) int64 {
	q, r := a/b, a%b
	if r != 0 && ((a > 0) == (b > 0)) {
		q++
	}
	return q
}

// divFloor is an explicit alias kept for readability at call sites.
func divFloor(a, b int64) int64 { return floorDiv(a, b) }
