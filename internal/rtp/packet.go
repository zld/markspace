package rtp

import (
	"errors"
	"fmt"

	"github.com/pion/rtp"

	"tracecheck/internal/model"
)

// ErrRTPHeader is returned when captured RTP bytes do not form a valid
// fixed RTP header.
var ErrRTPHeader = errors.New("invalid RTP header")

// rtpFixedHeader is the minimum RTP fixed header size in bytes.
const rtpFixedHeader = 12

// rtpVersion2 is the only RTP protocol version this kernel accepts.
const rtpVersion2 = 2

// ParsedPacket is the header view of one captured RTP datagram.
type ParsedPacket struct {
	Header  *rtp.Header
	Payload []byte
}

// ParseRTP decodes the fixed RTP header using pion/rtp and returns the
// header together with the codec payload. CSRC and extension bytes are
// accounted for by the library and excluded from the payload slice.
func ParseRTP(datagram []byte) (ParsedPacket, error) {
	if len(datagram) < rtpFixedHeader {
		return ParsedPacket{}, fmt.Errorf("%w: %d bytes", ErrRTPHeader, len(datagram))
	}
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(datagram); err != nil {
		return ParsedPacket{}, fmt.Errorf("%w: %v", ErrRTPHeader, err)
	}
	if pkt.Version != rtpVersion2 {
		return ParsedPacket{}, fmt.Errorf("%w: version %d", ErrRTPHeader, pkt.Version)
	}
	return ParsedPacket{Header: &pkt.Header, Payload: pkt.Payload}, nil
}

// RecordFromDatagram parses raw RTP bytes and fills the transport fields of
// a PacketRecord; codec and arrival metadata stay under the caller's
// control. A malformed datagram yields an rtp_malformed finding span at the
// supplied arrival tick rather than a half-populated record.
func RecordFromDatagram(stream uint32, codec model.CodecKind, datagram []byte, arrival model.Tick) (model.PacketRecord, error) {
	parsed, err := ParseRTP(datagram)
	if err != nil {
		return model.PacketRecord{Stream: stream, Codec: codec, ArrivalTick: arrival}, err
	}
	return model.PacketRecord{
		Stream:       stream,
		Codec:        codec,
		Sequence:     parsed.Header.SequenceNumber,
		RTPTimestamp: parsed.Header.Timestamp,
		Payload:      parsed.Payload,
		ArrivalTick:  arrival,
	}, nil
}
