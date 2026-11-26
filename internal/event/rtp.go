package event

import (
	"github.com/pion/rtp"

	"replayproof/internal/media"
)

// Packet is one decoded RTP trigger packet.
type Packet struct {
	SequenceNumber uint16
	Marker         bool
	Timestamp      uint32
	SSRC           uint32
	PayloadType    uint8
}

// ParsePacket decodes an RTP packet with pion/rtp. The full datagram is
// accepted so callers can feed captured or generated packets unchanged.
func ParsePacket(raw []byte) (Packet, error) {
	var p rtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return Packet{}, media.Wrap(err, media.CodeInvalidBox, "rtp.unmarshal", -1)
	}
	return Packet{
		SequenceNumber: p.SequenceNumber,
		Marker:         p.Marker,
		Timestamp:      p.Timestamp,
		SSRC:           p.SSRC,
		PayloadType:    p.PayloadType,
	}, nil
}

// MarshalPacket builds RTP bytes for fixtures and tests.
func MarshalPacket(p Packet) ([]byte, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         p.Marker,
			PayloadType:    p.PayloadType,
			SequenceNumber: p.SequenceNumber,
			Timestamp:      p.Timestamp,
			SSRC:           p.SSRC,
		},
		Payload: []byte{0x01},
	}
	buf, err := pkt.Marshal()
	if err != nil {
		return nil, media.Wrap(err, media.CodeInvalidBox, "rtp.marshal", -1)
	}
	return buf, nil
}

// Event is a distinct trigger formed by a marker-bit packet.
type Event struct {
	// Index is the 1-based ordinal of the event after duplicate suppression.
	Index int
	// Packet is the marker packet that created the event.
	Packet Packet
	// At is the event time on the media timeline.
	At media.Rational
	// Duplicates counts later packets that reused this sequence number and
	// were ignored.
	Duplicates int
}

// Stream converts a packet sequence at a clock rate into distinct events.
//
// Only packets with the marker bit set trigger. Re-delivery of a sequence
// number is ignored entirely: the first occurrence wins, whether or not it
// carried the marker. Marker timestamps that step backwards by less than
// half the 32-bit timestamp space are treated as reordering and clamped to
// the previous marker so the resulting windows stay ordered.
type Stream struct {
	ClockRate uint32
	Base      media.Rational
}

// Events returns the deduplicated marker events in arrival order.
func (s Stream) Events(packets []Packet) ([]Event, error) {
	if s.ClockRate == 0 {
		return nil, media.New(media.CodeConfig, "rtp.stream", -1, "zero clock rate")
	}
	firstSeen := make(map[uint16]int) // sequence -> event index or -1
	var events []Event
	var lastTS uint32
	var haveTS bool
	for _, p := range packets {
		if idx, ok := firstSeen[p.SequenceNumber]; ok {
			if idx >= 0 {
				events[idx].Duplicates++
			}
			continue
		}
		firstSeen[p.SequenceNumber] = -1
		if !p.Marker {
			continue
		}
		if haveTS && int32(p.Timestamp-lastTS) < 0 {
			p.Timestamp = lastTS
		}
		ev := Event{
			Index:  len(events) + 1,
			Packet: p,
			At:     s.Base.Add(media.FromTicks(int64(p.Timestamp), s.ClockRate)),
		}
		firstSeen[p.SequenceNumber] = len(events)
		events = append(events, ev)
		lastTS = p.Timestamp
		haveTS = true
	}
	return events, nil
}
