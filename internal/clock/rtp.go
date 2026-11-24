package clock

import "fmt"

// Unwrapper extends 32 bit RTP timestamps into a monotonic int64 clock.
type Unwrapper struct {
	last    uint32
	cycles  int64
	started bool
}

// Extend folds a 32 bit timestamp into the extended clock, handling wrap in
// both directions. A zero difference is treated as a duplicate and returns the
// current extended value.
func (u *Unwrapper) Extend(ts uint32) (int64, error) {
	if !u.started {
		u.started = true
		u.last = ts
		return int64(ts), nil
	}
	diff := int64(int32(ts - u.last))
	if diff > 0 && ts < u.last {
		u.cycles += 1 << 32
	} else if diff < 0 && ts > u.last {
		u.cycles -= 1 << 32
	}
	u.last = ts
	ext, ok := addChecked(int64(ts), u.cycles)
	if !ok {
		return 0, ErrWrapAmbiguous
	}
	return ext, nil
}

// Converter maps RTP clock ticks onto the media timeline for one audio track.
type Converter struct {
	Timeline  *Timeline
	ClockRate uint32
	StartRTP  int64 // extended RTP tick anchored as the first observed packet
	Offset    int64 // local media tick at StartRTP
	unwrap    Unwrapper
	primed    bool
}

// Prime anchors the converter at the first observed RTP timestamp.
func (c *Converter) Prime(ts uint32) (int64, error) {
	if c.ClockRate == 0 {
		return 0, fmt.Errorf("rtp clock rate must be non-zero")
	}
	ext, err := c.unwrap.Extend(ts)
	if err != nil {
		return 0, err
	}
	c.StartRTP = ext
	c.primed = true
	return c.Offset, nil
}

// LocalTick converts an RTP timestamp into local media ticks.
func (c *Converter) LocalTick(ts uint32) (int64, error) {
	if !c.primed {
		return c.Prime(ts)
	}
	ext, err := c.unwrap.Extend(ts)
	if err != nil {
		return 0, err
	}
	delta := ext - c.StartRTP
	return c.Offset + mulDivRound(delta, int64(c.Timeline.Timescale), int64(c.ClockRate)), nil
}
