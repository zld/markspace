// Package rtptrace maps a recovered media timeline onto RTP sequence numbers
// and timestamps and applies reproducible weak-network impairments. The source
// sample byte offsets are carried through unchanged.
package rtptrace

import (
	"github.com/pion/rtp"

	"tracecheck/internal/clock"
	"tracecheck/internal/probe"
)

// Packet is one RTP packet tied back to the media sample it carries.
type Packet struct {
	Sequence  uint16
	Timestamp uint32 // RTP clock equals the shared 90 kHz grid
	SSRC      uint32
	Payload   []byte
	// SourceOffset is the original input byte position of the media sample.
	SourceOffset clock.Offset
	SampleIndex  uint32
	Sync         bool
}

// Marshal encodes the packet using pion/rtp, guaranteeing on-wire compatibility.
func (p Packet) Marshal() ([]byte, error) {
	header := rtp.Header{
		Version:        2,
		PayloadType:    PayloadType,
		SequenceNumber: p.Sequence,
		Timestamp:      p.Timestamp,
		SSRC:           p.SSRC,
		Marker:         p.Sync,
	}
	pkt := &rtp.Packet{Header: header, Payload: p.Payload}
	return pkt.Marshal()
}

// PayloadType is fixed for the synthetic trace so reruns stay comparable.
const PayloadType = 96

// TimelineOptions configures one deterministic RTP mapping.
type TimelineOptions struct {
	SSRC          uint32
	StartSequence uint16
}

// FromTimeline converts recovered samples into ordered RTP packets. Timestamps
// come straight from the shared 90 kHz ticks; no timescale math happens here.
// Sequence numbers wrap mod 2^16 like a real RTP session.
func FromTimeline(samples []probe.RecoveredSample, opts TimelineOptions) []Packet {
	out := make([]Packet, 0, len(samples))
	for i, s := range samples {
		out = append(out, Packet{
			Sequence:     opts.StartSequence + uint16(i),
			Timestamp:    uint32(s.Start),
			SSRC:         opts.SSRC,
			Payload:      payloadRef(s),
			SourceOffset: s.Offset,
			SampleIndex:  s.Index,
			Sync:         s.Sync,
		})
	}
	return out
}

// payloadRef builds a small deterministic payload from the sample identity.
func payloadRef(s probe.RecoveredSample) []byte {
	buf := make([]byte, 8)
	buf[0] = byte(s.TrackID)
	buf[1] = byte(s.TrackID >> 8)
	buf[2] = byte(s.Index)
	buf[3] = byte(s.Index >> 8)
	buf[4] = byte(s.Offset)
	buf[5] = byte(s.Offset >> 8)
	buf[6] = byte(s.Offset >> 16)
	buf[7] = byte(s.Offset >> 24)
	return buf
}

// ParseRoundTrip marshals and immediately parses a packet with pion/rtp so
// tests can prove the wire encoding preserves sequence and timestamp.
func ParseRoundTrip(p Packet) (*rtp.Packet, error) {
	raw, err := p.Marshal()
	if err != nil {
		return nil, err
	}
	got := &rtp.Packet{}
	if err := got.Unmarshal(raw); err != nil {
		return nil, err
	}
	return got, nil
}
