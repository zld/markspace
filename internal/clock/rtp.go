package clock

import (
	"errors"
	"fmt"

	"github.com/pion/rtp"
)

// ErrRTP is returned for RTP inputs that cannot be interpreted.
var ErrRTP = errors.New("invalid rtp timing")

// FromRTP converts an RTP timestamp into the shared rational media clock.
// The timestamp is an unsigned 32-bit counter at clockRate, so the conversion
// is a single integer-ratio step with no accumulated error.
func FromRTP(pkt *rtp.Packet, clockRate uint32) (Time, Rational, error) {
	if pkt == nil {
		return Time{}, Rational{}, fmt.Errorf("%w: nil packet", ErrRTP)
	}
	if clockRate == 0 {
		return Time{}, Rational{}, fmt.Errorf("%w: zero clock rate", ErrRTP)
	}
	t := Time{Ticks: int64(pkt.Timestamp), Timescale: clockRate}
	return t, t.Rational(), nil
}

// RTPClock tracks sequence numbers and timestamps to detect loss and gaps in
// an RTP stream that feeds the same media timeline.
type RTPClock struct {
	ClockRate uint32
	FirstSeq  uint16
	LastSeq   uint16
	FirstTS   uint32
	LastTS    uint32
	Count     int
	// SequenceGaps counts missing sequence numbers.
	SequenceGaps int
	// TimestampJumps records non-monotonic timestamp movements in ticks.
	TimestampJumps []int64
	seen           bool
}

// NewRTPClock returns a clock for the given rate.
func NewRTPClock(clockRate uint32) *RTPClock { return &RTPClock{ClockRate: clockRate} }

// Add consumes one packet, updating sequence and timestamp bookkeeping.
func (c *RTPClock) Add(pkt *rtp.Packet) error {
	if pkt == nil {
		return fmt.Errorf("%w: nil packet", ErrRTP)
	}
	if c.ClockRate == 0 {
		return fmt.Errorf("%w: zero clock rate", ErrRTP)
	}
	if !c.seen {
		c.seen = true
		c.FirstSeq, c.LastSeq = pkt.SequenceNumber, pkt.SequenceNumber
		c.FirstTS, c.LastTS = pkt.Timestamp, pkt.Timestamp
		c.Count = 1
		return nil
	}
	// 16-bit sequence arithmetic: a forward step of one is contiguous.
	delta := int32(int16(pkt.SequenceNumber - c.LastSeq))
	if delta <= 0 {
		return fmt.Errorf("%w: non-monotonic sequence %d after %d", ErrRTP, pkt.SequenceNumber, c.LastSeq)
	}
	if delta > 1 {
		c.SequenceGaps += int(delta) - 1
	}
	if int32(pkt.Timestamp-c.LastTS) < 0 {
		c.TimestampJumps = append(c.TimestampJumps, int64(int32(c.LastTS-pkt.Timestamp)))
	}
	c.LastSeq = pkt.SequenceNumber
	c.LastTS = pkt.Timestamp
	c.Count++
	return nil
}

// Elapsed returns the exact time covered by the timestamps observed.
func (c *RTPClock) Elapsed() (Time, bool) {
	if !c.seen || c.ClockRate == 0 {
		return Time{}, false
	}
	return Time{Ticks: int64(c.LastTS) - int64(c.FirstTS), Timescale: c.ClockRate}, true
}

// Start returns the first timestamp as a clock.Time.
func (c *RTPClock) Start() (Time, bool) {
	if !c.seen {
		return Time{}, false
	}
	return Time{Ticks: int64(c.FirstTS), Timescale: c.ClockRate}, true
}
