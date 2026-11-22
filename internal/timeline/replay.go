package timeline

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"mediapipeline/internal/media"
)

// Replay recomputes sealed segments deterministically from the full event
// history. It is the reference implementation the streaming Timeline is
// checked against: feeding the same events in any order must yield the same
// segment hashes.
type Replay struct {
	segment media.Nano
	maxLen  media.Nano
}

// NewReplay validates replay bounds. maxLen bounds the total span that may be
// re-derived in one operation.
func NewReplay(segment, maxLen media.Nano) (*Replay, error) {
	if segment <= 0 {
		return nil, media.NewError(media.InvalidInput, "replay segment duration must be positive")
	}
	if maxLen <= 0 {
		return nil, media.NewError(media.InvalidInput, "replay length limit must be positive")
	}
	return &Replay{segment: segment, maxLen: maxLen}, nil
}

// Window describes the interval a replay covers.
type Window struct {
	Start media.Nano
	End   media.Nano
}

// Result is the outcome of one replay.
type Result struct {
	Window   Window
	Segments []Segment
	// Truncated is true when an event fell outside the replay window and the
	// caller must widen the window or fail.
	Truncated bool
}

// Recompute re-derives every segment overlapping [from, to) from events.
// Events outside the window are ignored but recorded via Truncated so callers
// never silently lose data.
func (r *Replay) Recompute(events []Event, from, to media.Nano) (Result, error) {
	if to <= from {
		return Result{}, media.NewError(media.InvalidInput,
			"replay window [%d,%d) is empty", int64(from), int64(to))
	}
	if to-from > r.maxLen {
		return Result{}, media.NewError(media.ReplayLimitExceeded,
			"replay window of %d ns exceeds limit %d ns", int64(to-from), int64(r.maxLen))
	}
	first := int(from / r.segment)
	last := int((to - 1) / r.segment)
	res := Result{Window: Window{Start: media.Nano(first) * r.segment, End: media.Nano(last+1) * r.segment}}
	buckets := make(map[int][]Event, last-first+1)
	for _, ev := range events {
		if ev.Time < from || ev.Time >= to {
			res.Truncated = true
			continue
		}
		idx := int(ev.Time / r.segment)
		buckets[idx] = append(buckets[idx], ev)
	}
	for idx := first; idx <= last; idx++ {
		start := media.Nano(idx) * r.segment
		seg := Segment{Index: idx, Start: start, End: start + r.segment}
		if evs, ok := buckets[idx]; ok {
			sortEvents(evs)
			seg.Events = evs
		}
		res.Segments = append(res.Segments, seg)
	}
	return res, nil
}

// HashSegments computes a stable digest over a segment sequence. It covers
// indexes, bounds and the canonical event order, so any change in output is
// visible as a different hash.
func HashSegments(segs []Segment) string {
	h := sha256.New()
	var buf [8]byte
	put := func(v int64) {
		binary.BigEndian.PutUint64(buf[:], uint64(v))
		h.Write(buf[:])
	}
	put(int64(len(segs)))
	for _, s := range segs {
		put(int64(s.Index))
		put(int64(s.Start))
		put(int64(s.End))
		put(int64(len(s.Events)))
		for _, ev := range s.Events {
			put(int64(ev.Time))
			put(int64(ev.Track))
			put(int64(ev.Seq))
			h.Write([]byte(ev.Payload))
			h.Write([]byte{0})
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// CrossCheck replays the events in the given sealed segments and verifies the
// timeline's own ordering is canonical. It returns the recomputed segments and
// their hash; the caller compares against HashSegments(tl.Segments()).
func (r *Replay) CrossCheck(tl *Timeline) (Result, string, error) {
	segs := tl.Segments()
	events := make([]Event, 0, tl.EventCount())
	for _, s := range segs {
		events = append(events, s.Events...)
	}
	if len(segs) == 0 {
		return Result{}, HashSegments(nil), nil
	}
	from := segs[0].Start
	to := segs[len(segs)-1].End
	res, err := r.Recompute(events, from, to)
	if err != nil {
		return Result{}, "", err
	}
	if res.Truncated {
		return Result{}, "", media.NewError(media.InvariantViolation,
			"sealed events fall outside their own segment bounds")
	}
	return res, HashSegments(res.Segments), nil
}
