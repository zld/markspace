// Package sim generates deterministic, replayable sender streams with network
// impairments: burst loss, reordering, duplication, jitter and corrupt
// datagrams. All randomness comes from a seeded math/rand source.
package sim

import (
	"fmt"
	"time"
)

// EventKind labels an impairment applied to a generated datagram.
type EventKind string

const (
	EventOK        EventKind = "ok"
	EventLost      EventKind = "lost"
	EventDuplicate EventKind = "duplicate"
	EventCorrupt   EventKind = "corrupt"
	EventBadVer    EventKind = "bad_version"
	EventBadLen    EventKind = "bad_length"
	EventAmbiguous EventKind = "ambiguous_seq"
)

// Event is one delivery (or non-delivery) on the simulated link.
type Event struct {
	FrameNo  uint32
	Seq      uint16
	ArriveNS time.Duration // logical receive-clock arrival time
	Kind     EventKind
	Datagram []byte // nil for lost events
}

// Config controls impairment generation.
type Config struct {
	Frames        int
	Seed          int64
	LossBurst     int     // maximum burst length; 1 disables burst loss
	LossRate      float64 // probability a burst starts at any frame
	ReorderWindow int     // maximum frames an out-of-order packet is delayed
	DuplicateRate float64
	CorruptRate   float64
	JitterNS      time.Duration // zero-mean uniform arrival jitter
}

// DefaultConfig returns representative settings.
func DefaultConfig() Config {
	return Config{
		Frames: 1000, Seed: 1, LossBurst: 4, LossRate: 0.02,
		ReorderWindow: 8, DuplicateRate: 0.02, CorruptRate: 0.005,
		JitterNS: 15 * time.Millisecond,
	}
}

// Validate checks ranges and conflicting parameters.
func (c Config) Validate() error {
	if c.Frames <= 0 {
		return errf("frames must be positive, got %d", c.Frames)
	}
	if c.LossBurst < 0 {
		return errf("loss-burst must be >= 0, got %d", c.LossBurst)
	}
	if c.LossRate < 0 || c.LossRate > 1 {
		return errf("loss-rate out of range [0,1]: %v", c.LossRate)
	}
	if c.DuplicateRate < 0 || c.DuplicateRate > 1 {
		return errf("duplicate out of range: %v", c.DuplicateRate)
	}
	if c.CorruptRate < 0 || c.CorruptRate > 1 {
		return errf("corrupt out of range: %v", c.CorruptRate)
	}
	if c.ReorderWindow < 0 {
		return errf("reorder must be >= 0, got %d", c.ReorderWindow)
	}
	if c.JitterNS < 0 {
		return errf("jitter must be >= 0")
	}
	if c.LossRate+c.DuplicateRate+c.CorruptRate > 1.5 {
		return errf("impairment rates conflict: combined probability %.3f > 1.5",
			c.LossRate+c.DuplicateRate+c.CorruptRate)
	}
	return nil
}

type simError struct{ msg string }

func (e *simError) Error() string { return e.msg }

func errf(format string, args ...any) error {
	return &simError{msg: fmt.Sprintf(format, args...)}
}
