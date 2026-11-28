// Package rtp turns a jittery, lossy RTP packet stream into an ordered frame
// timeline on the shared nanosecond clock.
//
// The window reassembles packets in extended sequence-number order, recognises
// 16-bit wraparound, drops duplicates idempotently and reports gaps. Frames are
// placed on the timeline from their RTP timestamps, so a timestamp
// discontinuity is *reported* rather than silently smoothed away. Recovery
// (see recover.go) fills each gap with silence of exactly the missing duration.
package rtp

import (
	"fmt"

	"github.com/pion/rtp"

	"mediarc/model"
	"mediarc/timebase"
)

const stream = "rtp"

// Arrival is one packet handed to the window, as it came off the wire.
type Arrival struct {
	// Packet is the parsed RTP packet. Only Header and Payload are read.
	Packet *rtp.Packet
	// ReceivedNS is the local arrival time on the shared timeline. It is
	// recorded for jitter reporting and does not move media time.
	ReceivedNS int64
}

const (
	// DefaultPacketDurationNS is the media duration carried by one packet.
	DefaultPacketDurationNS = 20 * 1_000_000 // 20 ms
	// DefaultClockRate is the RTP timestamp clock, as used by 48 kHz audio.
	DefaultClockRate = 90000
)

// Config bounds the jitter window and fixes its media clock.
type Config struct {
	// Depth is the maximum number of frames the window may hold before it
	// must release and declare a gap. Must be positive.
	Depth int
	// PacketDurationNS is the media duration carried by one packet. Zero
	// means DefaultPacketDurationNS.
	PacketDurationNS int64
	// ClockRate is the RTP timestamp clock in Hz. Zero means
	// DefaultClockRate.
	ClockRate uint32
	// TimestampJumps makes a non-uniform timestamp step a reported issue
	// instead of a silently accepted packet. The frame is still placed using
	// its own timestamp.
	TimestampJumps bool
}

func (c Config) validate() (Config, error) {
	if c.Depth <= 0 {
		return Config{}, model.New(model.CodeInvalidArgument, stream,
			fmt.Sprintf("window depth must be positive, got %d", c.Depth))
	}
	if c.PacketDurationNS < 0 {
		return Config{}, model.New(model.CodeInvalidArgument, stream,
			fmt.Sprintf("packet duration must not be negative, got %d", c.PacketDurationNS))
	}
	if c.PacketDurationNS == 0 {
		c.PacketDurationNS = DefaultPacketDurationNS
	}
	if c.ClockRate == 0 {
		c.ClockRate = DefaultClockRate
	}
	if _, err := timebase.NanosFromScale(1, c.ClockRate); err != nil {
		return Config{}, model.New(model.CodeInvalidArgument, stream, "clock rate is not representable")
	}
	return c, nil
}

// expectedTicks is the RTP timestamp step between consecutive packets.
func (c Config) expectedTicks() int64 {
	if c.PacketDurationNS <= 0 {
		return 0
	}
	return c.PacketDurationNS * int64(c.ClockRate) / 1_000_000_000
}

// duplicateMemory bounds the delivered-sequence history used for idempotent
// duplicate detection, so memory stays proportional to the window depth.
func (c Config) duplicateMemory() int { return 4*c.Depth + 16 }

// Outcome classifies how the window treated one arrival.
type Outcome int

const (
	// Accepted means the packet extended the ordered timeline.
	Accepted Outcome = iota
	// Reordered means the packet arrived after a higher sequence number but
	// was still inside the window and was inserted in order.
	Reordered
	// Duplicate means the packet was already seen; nothing changed.
	Duplicate
	// Late means the packet fell before the released floor and was dropped.
	Late
	// Invalid means the packet could not be placed.
	Invalid
)

// String renders the outcome for reports.
func (o Outcome) String() string {
	switch o {
	case Accepted:
		return "accepted"
	case Reordered:
		return "reordered"
	case Duplicate:
		return "duplicate"
	case Late:
		return "late"
	case Invalid:
		return "invalid"
	default:
		return "unknown"
	}
}

// Stats summarises what the window did.
type Stats struct {
	Arrived     int
	Accepted    int
	Reordered   int
	Duplicates  int
	Late        int
	Invalid     int
	PacketsLost uint64
}

// Sub returns the counter change between two snapshots.
func (s Stats) Sub(prev Stats) Stats {
	return Stats{
		Arrived:     s.Arrived - prev.Arrived,
		Accepted:    s.Accepted - prev.Accepted,
		Reordered:   s.Reordered - prev.Reordered,
		Duplicates:  s.Duplicates - prev.Duplicates,
		Late:        s.Late - prev.Late,
		Invalid:     s.Invalid - prev.Invalid,
		PacketsLost: s.PacketsLost - prev.PacketsLost,
	}
}
