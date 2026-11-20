// Package plc generates concealment frames and tracks hidden-frame runs.
package plc

import "encoding/binary"

// Stats is an immutable snapshot of a concealer.
type Stats struct {
	TotalFrames  int64
	RealFrames   int64
	HiddenFrames int64
	CurrentRun   int64
	MaxRun       int64
	Degraded     bool
	Degradations int64
}

// Ratio reports the fraction of delivered frames that were real.
func (s Stats) Ratio() float64 {
	if s.TotalFrames == 0 {
		return 0
	}
	return float64(s.RealFrames) / float64(s.TotalFrames)
}

// Concealer tracks consecutive missing frames for one stream. Reaching the
// degradation threshold marks the run as degraded; a real frame ends the run
// and clears the degraded state.
type Concealer struct {
	threshold    int64
	currentRun   int64
	maxRun       int64
	totalFrames  int64
	realFrames   int64
	hiddenFrames int64
	degraded     bool
	degradations int64
}

// New creates a concealment tracker. A threshold below one is treated as one.
func New(threshold int) *Concealer {
	if threshold < 1 {
		threshold = 1
	}
	return &Concealer{threshold: int64(threshold)}
}

// RealFrame records that a genuine frame was delivered.
func (c *Concealer) RealFrame() {
	c.totalFrames++
	c.realFrames++
	c.currentRun = 0
	c.degraded = false
}

// HiddenFrame records a concealed frame and returns a deterministic synthetic
// payload carrying the stream and the missing sequence number.
func (c *Concealer) HiddenFrame(streamID uint16, seq uint32) []byte {
	c.totalFrames++
	c.hiddenFrames++
	c.currentRun++
	if c.currentRun > c.maxRun {
		c.maxRun = c.currentRun
	}
	if c.currentRun == c.threshold {
		c.degraded = true
		c.degradations++
	}
	payload := make([]byte, 20)
	binary.BigEndian.PutUint16(payload[0:2], streamID)
	binary.BigEndian.PutUint32(payload[2:6], seq)
	copy(payload[6:], "PLC-FRAME")
	return payload
}

// Reset ends the current hidden run without discarding totals. It is used when
// a stream is treated as interrupted and later resumes.
func (c *Concealer) Reset() {
	c.currentRun = 0
	c.degraded = false
}

// Stats returns a snapshot for aggregation.
func (c *Concealer) Stats() Stats {
	return Stats{
		TotalFrames:  c.totalFrames,
		RealFrames:   c.realFrames,
		HiddenFrames: c.hiddenFrames,
		CurrentRun:   c.currentRun,
		MaxRun:       c.maxRun,
		Degraded:     c.degraded,
		Degradations: c.degradations,
	}
}
