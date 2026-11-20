// Package timeline maps cue times to frame numbers and resolves overlaps.
package timeline

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"subx/internal/cue"
)

// FPS is a frame rate expressed as a rational number Num/Den.
type FPS struct {
	Num int64
	Den int64
}

// ParseFPS accepts an integer ("24", "30") or a rational ("24000/1001").
func ParseFPS(s string) (FPS, error) {
	parts := strings.Split(strings.TrimSpace(s), "/")
	if len(parts) > 2 {
		return FPS{}, fmt.Errorf("invalid fps %q: want N or N/D", s)
	}
	num, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || num <= 0 {
		return FPS{}, fmt.Errorf("invalid fps %q: numerator must be a positive integer", s)
	}
	den := int64(1)
	if len(parts) == 2 {
		den, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || den <= 0 {
			return FPS{}, fmt.Errorf("invalid fps %q: denominator must be a positive integer", s)
		}
	}
	f := FPS{Num: num, Den: den}
	if f.Num > 1000*f.Den {
		return FPS{}, fmt.Errorf("invalid fps %q: rates above 1000 fps are not supported", s)
	}
	return f, nil
}

func (f FPS) scale() int64 { return f.Den * 1000 } // ms units per frame-second

// StartFrame maps a millisecond time to its containing frame (floor).
func (f FPS) StartFrame(ms int64) int64 {
	return ms * f.Num / f.scale()
}

// EndFrame maps a millisecond time to the exclusive end frame (ceil).
func (f FPS) EndFrame(ms int64) int64 {
	return (ms*f.Num + f.scale() - 1) / f.scale()
}

// FrameStartMs renders the earliest ms timestamp that maps back to frame.
func (f FPS) FrameStartMs(frame int64) int64 {
	return (frame*f.scale() + f.Num - 1) / f.Num
}

// FrameEndMs renders the latest ms timestamp that maps back to frame.
func (f FPS) FrameEndMs(frame int64) int64 {
	return frame * f.scale() / f.Num
}

// FrameCue is a cue with its half-open frame interval and original times.
type FrameCue struct {
	cue.Cue
	StartFrame int64
	EndFrame   int64 // exclusive
	Trimmed    bool  // start was moved by the trim overlap policy
}

// Map converts cues to frame intervals, keeping at least one frame each.
func Map(cues []cue.Cue, fps FPS) []FrameCue {
	out := make([]FrameCue, len(cues))
	for i, c := range cues {
		sf, ef := fps.StartFrame(c.StartMs), fps.EndFrame(c.EndMs)
		if ef <= sf {
			ef = sf + 1
		}
		out[i] = FrameCue{Cue: c, StartFrame: sf, EndFrame: ef}
	}
	return out
}

// Policy selects how overlapping frame intervals are handled.
type Policy int

const (
	Keep Policy = iota
	Trim
	Reject
)

// ParsePolicy validates an --overlap value.
func ParsePolicy(s string) (Policy, error) {
	switch s {
	case "keep":
		return Keep, nil
	case "trim":
		return Trim, nil
	case "error":
		return Reject, nil
	}
	return Keep, fmt.Errorf("invalid overlap policy %q: want keep, trim or error", s)
}

// OverlapError reports cues rejected by the error policy.
type OverlapError struct{ Msg string }

func (e *OverlapError) Error() string { return e.Msg }

// Resolve applies the overlap policy. It returns the kept cues (sorted by
// start frame, stable by input order) and the cues dropped by trimming.
func Resolve(mapped []FrameCue, pol Policy) (kept, dropped []FrameCue, err error) {
	sorted := make([]FrameCue, len(mapped))
	copy(sorted, mapped)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].StartFrame < sorted[j].StartFrame })
	if pol == Keep {
		return sorted, nil, nil
	}
	var maxEnd int64
	for _, c := range sorted {
		if c.StartFrame < maxEnd {
			if pol == Reject {
				return nil, nil, &OverlapError{Msg: fmt.Sprintf(
					"cue %q starts at frame %d before previous cue ends at frame %d",
					c.ID, c.StartFrame, maxEnd)}
			}
			c.StartFrame = maxEnd
			c.Trimmed = true
		}
		if c.EndFrame <= c.StartFrame {
			dropped = append(dropped, c)
			continue
		}
		if c.EndFrame > maxEnd {
			maxEnd = c.EndFrame
		}
		kept = append(kept, c)
	}
	return kept, dropped, nil
}
