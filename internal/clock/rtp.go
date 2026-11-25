package clock

import (
	"mediad/internal/model"

	"github.com/pion/rtp"
)

// RTP clock rates. Video class RTP timestamps normally run at 90 kHz; the
// factor converts them to the shared 27 MHz media timeline (90000 * 300).
const rtpToMediaTicks = int64(model.ClockHz / 90000) // 300

// RTPClock reuses pion/rtp header parsing instead of hand-rolling the RTP
// bit layout. It unwraps the 32-bit RTP timestamp and 16-bit sequence number
// per synchronization source and exposes both on the media tick base.
type RTPClock struct {
	lastSeq      uint16
	seqStarted   bool
	totalPackets uint64
	lostPackets  uint64
	lastTS       uint32
	tsStarted    bool
	tsUnwrapped  int64
	tsBase       int64
	events       []ClockEvent
}

// NewRTPClock creates an empty RTP clock.
func NewRTPClock() *RTPClock { return &RTPClock{} }

// Ingest parses one RTP packet, checks its sequence number for gaps, unwraps
// the timestamp and returns the media-time equivalent in 27 MHz ticks.
func (c *RTPClock) Ingest(packet []byte, offset int64) (int64, error) {
	h := &rtp.Header{}
	if _, err := h.Unmarshal(packet); err != nil {
		return 0, &model.CorruptError{
			Kind: "rtp", Offset: offset, Detail: err.Error(),
		}
	}
	c.totalPackets++
	if c.seqStarted {
		want := uint16(c.lastSeq + 1)
		if h.SequenceNumber != want {
			gap := int(want) - int(h.SequenceNumber)
			if gap < 0 {
				gap = -gap
			}
			c.lostPackets += uint64(gap)
			c.events = append(c.events, ClockEvent{
				Kind: "rtp", Offset: offset, Delta: int64(gap),
			})
		}
	}
	c.seqStarted = true
	c.lastSeq = uint16(h.SequenceNumber)

	ts := h.Timestamp
	if !c.tsStarted {
		c.tsStarted = true
		c.lastTS = ts
		c.tsUnwrapped = int64(ts)
		c.tsBase = c.tsUnwrapped
		return 0, nil
	}
	delta := int64(ts) - int64(c.lastTS)
	// Unsigned modular difference: a negative 32-bit delta means wrap unless
	// the backward distance is genuinely shorter.
	if delta < 0 && delta+1<<32 <= 1<<31 {
		delta += 1 << 32
		c.events = append(c.events, ClockEvent{
			Kind: "wrap", Offset: offset, Delta: delta,
		})
	}
	c.lastTS = ts
	c.tsUnwrapped += delta
	return (c.tsUnwrapped - c.tsBase) * rtpToMediaTicks, nil
}

// LostPackets reports how many sequence numbers were skipped.
func (c *RTPClock) LostPackets() uint64 { return c.lostPackets }

// Events returns RTP-level discontinuity events.
func (c *RTPClock) Events() []ClockEvent { return c.events }
