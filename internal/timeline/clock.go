// Package timeline normalises per-track Matroska timecodes onto one nanosecond
// timebase, expands 32-bit wraparound timestamps, and models RTP sequence
// boundaries. VFR frame spacing is preserved: no resampling is performed.
package timeline

import (
	"fmt"

	"github.com/pion/rtp"
)

// TrackKind distinguishes the only track classes this kernel handles.
type TrackKind uint8

const (
	KindVideo TrackKind = iota + 1
	KindAudio
)

func (k TrackKind) String() string {
	switch k {
	case KindVideo:
		return "video"
	case KindAudio:
		return "audio"
	default:
		return fmt.Sprintf("kind(%d)", k)
	}
}

// TrackConfig describes one Matroska track's clock. TicksPerSecond converts
// raw timecode ticks to nanoseconds; WrapModulo is the 32-bit (or other)
// period after which raw timecodes wrap.
type TrackConfig struct {
	TrackNumber    uint64
	Kind           TrackKind
	TicksPerSecond uint64
	WrapModulo     uint64 // 0 => no wraparound
}

// Frame is one block after timebase normalisation. RawTicks is the original
// timecode; Ticks is the unwrapped monotonic value; T0 is nanoseconds.
type Frame struct {
	TrackNumber uint64
	Kind        TrackKind
	RawTicks    uint64
	Ticks       int64
	T0          int64 // nanoseconds on the unified timebase
	Keyframe    bool
	ByteOffset  int64 // offset of the block header in the source file
}

// AmbiguousError marks a raw timestamp that cannot be unwrapped uniquely:
// the gap to its predecessor exceeds half a wrap period (or time moves
// backwards beyond one wrap), so alignment must refuse the material.
type AmbiguousError struct {
	TrackNumber uint64
	Offset      int64
	RawPrev     uint64
	RawCur      uint64
	Modulo      uint64
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("timeline: ambiguous unwrap on track %d at offset=%d raw %d->%d modulo=%d",
		e.TrackNumber, e.Offset, e.RawPrev, e.RawCur, e.Modulo)
}

// Clock unwraps and converts raw timecodes for a single track.
type Clock struct {
	cfg        TrackConfig
	nsPerTick  float64
	lastRaw    uint64
	lastTicks  int64
	haveLast   bool
	frameCount int64
}

// NewClock builds a Clock for cfg.
func NewClock(cfg TrackConfig) (*Clock, error) {
	if cfg.TicksPerSecond == 0 {
		return nil, fmt.Errorf("timeline: track %d has zero tick rate", cfg.TrackNumber)
	}
	return &Clock{cfg: cfg, nsPerTick: 1e9 / float64(cfg.TicksPerSecond)}, nil
}

// Config returns the track configuration.
func (c *Clock) Config() TrackConfig { return c.cfg }

// FrameCount returns the number of timestamps ingested so far.
func (c *Clock) FrameCount() int64 { return c.frameCount }

// Unwrap expands a raw (possibly wrapped) timecode into a monotonic int64
// tick count. A delta whose magnitude reaches half the wrap period is
// inherently ambiguous between a forward and backward jump and fails closed.
func (c *Clock) Unwrap(raw uint64, byteOffset int64) (int64, error) {
	if !c.haveLast {
		t := int64(raw)
		c.lastRaw, c.lastTicks, c.haveLast, c.frameCount = raw, t, true, 1
		return t, nil
	}
	mod := c.cfg.WrapModulo
	if mod == 0 {
		if raw < c.lastRaw {
			return 0, &AmbiguousError{c.cfg.TrackNumber, byteOffset, c.lastRaw, raw, 0}
		}
		t := c.lastTicks + int64(raw-c.lastRaw)
		c.advance(raw, t)
		return t, nil
	}
	// Unsigned modular distance mapped to the shortest signed delta.
	// Add the period first to avoid unsigned underflow on backwards steps.
	d := raw + mod - c.lastRaw
	if d >= mod {
		d -= mod
	}
	half := int64(mod / 2)
	var delta int64
	if d > mod/2 {
		delta = int64(d) - int64(mod)
	} else {
		delta = int64(d)
	}
	// |delta| reaching half a period cannot be oriented uniquely.
	if delta <= -half || delta >= half {
		return 0, &AmbiguousError{c.cfg.TrackNumber, byteOffset, c.lastRaw, raw, mod}
	}
	t := c.lastTicks + delta
	c.advance(raw, t)
	return t, nil
}

func (c *Clock) advance(raw uint64, ticks int64) {
	c.lastRaw, c.lastTicks, c.frameCount = raw, ticks, c.frameCount+1
}

// ToNanos converts unwrapped ticks to the unified nanosecond timebase.
func (c *Clock) ToNanos(ticks int64) int64 {
	return int64(float64(ticks) * c.nsPerTick)
}

// Normalise unwraps a raw timecode and returns the resulting Frame template
// (caller fills payload-specific fields such as Keyframe).
func (c *Clock) Normalise(raw uint64, byteOffset int64) (Frame, error) {
	ticks, err := c.Unwrap(raw, byteOffset)
	if err != nil {
		return Frame{}, err
	}
	return Frame{
		TrackNumber: c.cfg.TrackNumber,
		Kind:        c.cfg.Kind,
		RawTicks:    raw,
		Ticks:       ticks,
		T0:          c.ToNanos(ticks),
		ByteOffset:  byteOffset,
	}, nil
}

// SeqCursor models RTP 16-bit sequence number boundaries with pion/rtp
// packets: wraparound at 0xFFFF->0x0000, gap and duplicate detection.
type SeqCursor struct {
	last    uint16
	started bool
	cycles  uint64
	total   uint64
}

// Observe feeds the sequence number of one packet. The returned distance is
// the signed forward distance from the previous packet (1 = consecutive,
// 0 = duplicate, >1 = gap). A negative distance means reordering beyond
// 65535/2 and is reported as an error.
func (s *SeqCursor) Observe(p *rtp.Packet) (dist int, err error) {
	cur := p.SequenceNumber
	if !s.started {
		s.started, s.last, s.total = true, cur, 1
		return 1, nil
	}
	d := int(int16(cur - s.last))
	if d < 0 {
		return d, fmt.Errorf("timeline: RTP sequence moved backwards %d->%d", s.last, cur)
	}
	if cur == 0 && s.last == 0xFFFF {
		s.cycles++
	}
	if d > 1 {
		err = fmt.Errorf("timeline: RTP sequence gap of %d at seq=%d", d-1, cur)
	}
	s.last, s.total = cur, s.total+uint64(d)
	return d, err
}

// Cycles reports how many 0xFFFF->0x0000 wraps have been observed.
func (s *SeqCursor) Cycles() uint64 { return s.cycles }

// Extended returns the 64-bit extended sequence counter.
func (s *SeqCursor) Extended() uint64 { return s.total }
