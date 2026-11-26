package rtp

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"
)

// BuildFECPacket creates a small XOR parity record. The payload layout is
// self-describing so recovery does not depend on fixture-only metadata.
func BuildFECPacket(ssrc uint32, sequence uint16, groupID byte, sequences []uint16, payloads [][]byte) (*rtp.Packet, error) {
	if len(sequences) == 0 || len(sequences) != len(payloads) || len(sequences) > 64 {
		return nil, fmt.Errorf("invalid FEC group")
	}
	width := 0
	for _, p := range payloads {
		if len(p) > width {
			width = len(p)
		}
	}
	if width > 255 {
		return nil, fmt.Errorf("FEC payload too wide")
	}
	parity := make([]byte, width)
	for _, p := range payloads {
		for i := 0; i < len(p) && i < width; i++ {
			parity[i] ^= p[i]
		}
	}
	buf := make([]byte, 4+2*len(sequences)+width)
	buf[0] = fecMagic
	buf[1] = groupID
	buf[2] = byte(len(sequences))
	buf[3] = byte(width)
	for i, seq := range sequences {
		binary.BigEndian.PutUint16(buf[4+2*i:], seq)
	}
	copy(buf[4+2*len(sequences):], parity)
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: FECPayloadType, SequenceNumber: sequence, SSRC: ssrc, Marker: true},
		Payload: buf,
	}, nil
}

func parseFEC(payload []byte) (byte, []uint16, []byte, error) {
	if len(payload) < 4 || payload[0] != fecMagic {
		return 0, nil, nil, fmt.Errorf("bad FEC magic")
	}
	groupID := payload[1]
	count := int(payload[2])
	width := int(payload[3])
	headerEnd := 4 + 2*count
	if len(payload) != headerEnd+width {
		return 0, nil, nil, fmt.Errorf("bad FEC length")
	}
	seqs := make([]uint16, count)
	for i := 0; i < count; i++ {
		seqs[i] = binary.BigEndian.Uint16(payload[4+2*i:])
	}
	return groupID, seqs, payload[headerEnd:], nil
}

// NewMediaPacket is a compact helper used by fixtures and tests.
func NewMediaPacket(seq uint16, ts uint32, payload []byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: MediaPayloadType, SequenceNumber: seq, Timestamp: ts, SSRC: 0x17579}, Payload: payload}
}
