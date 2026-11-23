package transport

import (
	"fmt"

	"github.com/pion/rtp"

	"archivecheck/internal/model"
)

// RTPPayloadType is the dynamic payload type the fixture uses.
const RTPPayloadType uint8 = 96

// RTPSSRC is the SSRC the fixture stamps on every packet of a stream.
const RTPSSRC uint32 = 0x41524348 // "ARCH"

// MarshalRTP encodes a packet into RTP wire format using pion/rtp.
func MarshalRTP(p Packet, ssrc uint32) ([]byte, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    RTPPayloadType,
			SequenceNumber: p.Sequence,
			Timestamp:      p.Timestamp,
			SSRC:           ssrc,
			Marker:         p.Marker,
		},
		Payload: p.Payload,
	}
	buf, err := pkt.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal RTP: %w", err)
	}
	return buf, nil
}

// ParseRTP decodes RTP wire bytes with pion/rtp, rejecting malformed packets
// with a located fault rather than a panic.
func ParseRTP(raw []byte, source Source) (Packet, *model.Fault) {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(raw); err != nil {
		return Packet{}, model.NewFault(model.CodeTransportBadPayload, model.SeverityError, model.Source{
			Layer: "transport", Segment: source.Segment,
			Representation: source.Representation, File: source.File, Offset: -1,
		}, "cannot parse RTP packet: %v", err).WithCause(err).WithBytes(0, int64(len(raw)))
	}
	if pkt.Version != 2 {
		return Packet{}, model.NewFault(model.CodeTransportBadPayload, model.SeverityError, model.Source{
			Layer: "transport", Segment: source.Segment,
			Representation: source.Representation, File: source.File, Offset: -1,
		}, "RTP version %d is not supported", pkt.Version).WithBytes(0, int64(len(raw)))
	}
	return Packet{
		Sequence:  pkt.SequenceNumber,
		Timestamp: pkt.Timestamp,
		Marker:    pkt.Marker,
		Payload:   append([]byte(nil), pkt.Payload...),
	}, nil
}
