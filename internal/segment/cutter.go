// Package segment cuts a validated frame stream into segments on the
// canonical nanosecond time base. A new segment starts when either the gap
// between consecutive frames is a jump relative to the expected frame
// interval, or the configured maximum segment duration is reached.
//
// Frame ranges are half-open [FrameStart, FrameEnd): a boundary frame belongs
// to exactly one segment. Every internal boundary timestamp equals the
// capture timestamp of a real frame; only the final segment end extends one
// expected frame interval past the last frame, matching media duration.
package segment

import (
	"errors"
	"math"

	"example.com/continuity/internal/clock"
	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/frame"
)

// Config controls cutting behavior. Zero values select safe defaults except
// MaxSegmentNS, which must be positive.
type Config struct {
	// ExpectedIntervalNS is the nominal inter-frame interval (e.g. 40ms at
	// 25 fps). A gap strictly larger than ExpectedIntervalNS*JumpFactor cuts.
	ExpectedIntervalNS int64
	// JumpFactor is the relative gap threshold; 0 selects 2.0.
	JumpFactor float64
	// MaxSegmentNS caps a segment's span measured from its first frame.
	MaxSegmentNS int64
}

// Segment is one half-open run of frames.
type Segment struct {
	Index        int
	Track        string
	FrameStart   uint64 // sequence number of the first frame (inclusive)
	FrameEnd     uint64 // sequence number one past the last frame (exclusive)
	StartNS      int64  // capture timestamp of FrameStart
	EndNS        int64  // boundary timestamp; see package note
	Frames       []*frame.Record
	PayloadBytes int64
}

// Cutter is stateful; push frames in capture order.
type Cutter struct {
	cfg      Config
	current  *Segment
	prevNS   int64
	havePrev bool
	count    int
}

// New validates the configuration and returns a cutter.
func New(cfg Config) (*Cutter, error) {
	if cfg.MaxSegmentNS <= 0 {
		return nil, fault.New(fault.StageSegment, "config",
			errors.New("max segment duration must be positive"))
	}
	if cfg.ExpectedIntervalNS <= 0 {
		return nil, fault.New(fault.StageSegment, "config",
			errors.New("expected frame interval must be positive"))
	}
	if cfg.JumpFactor <= 0 {
		cfg.JumpFactor = 2.0
	}
	return &Cutter{cfg: cfg}, nil
}

func (c *Cutter) fail(rec *frame.Record, op string, cause error) *fault.Error {
	e := fault.New(fault.StageSegment, op, cause).AtTrack(rec.Track).AtFrame(int64(rec.Seq))
	e.Offset = rec.Offset
	return e
}

// jumpThreshold returns the absolute gap that counts as an interval jump.
func (c *Cutter) jumpThreshold() int64 {
	f := c.cfg.JumpFactor
	if f <= 1.0 {
		// Factor at or below 1 degenerates; clamp to one extra nanosecond so
		// a normal equal-length gap never cuts.
		return c.cfg.ExpectedIntervalNS + 1
	}
	t := float64(c.cfg.ExpectedIntervalNS) * f
	if t >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(t)
}

// Push appends a frame, returning a completed segment when a boundary is
// reached (the returned segment is nil otherwise).
func (c *Cutter) Push(rec *frame.Record) (*Segment, error) {
	if c.havePrev {
		if err := clock.CheckMonotonic(c.prevNS, rec.CaptureNS); err != nil {
			return nil, c.fail(rec, "monotonic", err)
		}
	}
	// Reject up front any timestamp whose segment-end arithmetic would
	// overflow. The frame is refused before the caller can commit a
	// checkpoint for it, so no overflowing frame is ever persisted.
	if _, err := clock.Add(rec.CaptureNS, c.cfg.ExpectedIntervalNS); err != nil {
		return nil, c.fail(rec, "timestamp-overflow", err)
	}
	var cut bool
	if c.current != nil {
		span, err := clock.Sub(rec.CaptureNS, c.current.StartNS)
		if err != nil {
			return nil, c.fail(rec, "span", err)
		}
		switch {
		case span >= c.cfg.MaxSegmentNS:
			cut = true
		case c.havePrev:
			gap, err := clock.Sub(rec.CaptureNS, c.prevNS)
			if err != nil {
				return nil, c.fail(rec, "gap", err)
			}
			if gap > c.jumpThreshold() {
				cut = true
			}
		}
	}
	var done *Segment
	if cut {
		// The boundary is the last timestamp inside the closing segment;
		// the next frame opens a new segment at its own capture instant.
		done = c.closeCurrent(c.prevNS)
	}
	if c.current == nil {
		c.current = &Segment{
			Index: c.count, Track: rec.Track,
			FrameStart: rec.Seq, StartNS: rec.CaptureNS,
		}
		c.count++
	}
	cur := c.current
	cur.Frames = append(cur.Frames, rec)
	cur.FrameEnd = rec.Seq + 1
	size, err := clock.Add(cur.PayloadBytes, int64(len(rec.Payload)))
	if err != nil {
		return nil, c.fail(rec, "payload-bytes", err)
	}
	cur.PayloadBytes = size
	c.prevNS = rec.CaptureNS
	c.havePrev = true
	return done, nil
}

// closeCurrent finalizes a segment ending at the boundary frame timestamp
// boundaryNS, which is the first timestamp of the segment that follows.
func (c *Cutter) closeCurrent(boundaryNS int64) *Segment {
	seg := c.current
	seg.EndNS = boundaryNS
	c.current = nil
	return seg
}

// Finish flushes the final segment. Its end is the last frame timestamp plus
// one expected interval; an overflow there is a structured fault.
func (c *Cutter) Finish() (*Segment, error) {
	if c.current == nil {
		return nil, nil
	}
	last := c.current.Frames[len(c.current.Frames)-1]
	end, err := clock.Add(last.CaptureNS, c.cfg.ExpectedIntervalNS)
	if err != nil {
		return nil, c.fail(last, "segment-end", err)
	}
	seg := c.current
	seg.EndNS = end
	c.current = nil
	return seg, nil
}
