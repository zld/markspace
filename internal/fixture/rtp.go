package fixture

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/pion/rtp/v2"
)

// WriteRTPFrames writes the local length-prefixed RTP packet container. Each
// frame is a uint32 big-endian length followed by a standard RTP packet.
func WriteRTPFrames(path string, packets []rtp.Packet) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var out []byte
	for _, p := range packets {
		raw, err := p.Marshal()
		if err != nil {
			return err
		}
		l := make([]byte, 4)
		binary.BigEndian.PutUint32(l, uint32(len(raw)))
		out = append(out, l...)
		out = append(out, raw...)
	}
	return os.WriteFile(path, out, 0o644)
}

// WrappedTimestampPackets builds packets whose 32-bit timestamp starts just
// before the wrap boundary and crosses it several times if requested.
func WrappedTimestampPackets(ssrc uint32, pt uint8, count int, start uint32, step uint32) []rtp.Packet {
	pkts := make([]rtp.Packet, 0, count+1)
	for i := 0; i < count; i++ {
		ts := start + uint32(i)*step
		pkts = append(pkts, rtp.Packet{Header: rtp.Header{
			Version: 2, Marker: i == count-1, PayloadType: pt,
			SequenceNumber: uint16(1000 + i), Timestamp: ts, SSRC: ssrc,
		}, Payload: Payload(ssrc, i, 40)})
	}
	// An exact duplicate of packet seven must be counted, never remuxed twice.
	if count > 7 {
		dup := pkts[7]
		pkts = append(pkts, dup)
	}
	return pkts
}

// GapPackets returns four packets with sequence 10,11,14,15: two missing.
func GapPackets(ssrc uint32, pt uint8) []rtp.Packet {
	seqs := []uint16{10, 11, 14, 15}
	out := make([]rtp.Packet, 0, len(seqs))
	for i, seq := range seqs {
		out = append(out, rtp.Packet{Header: rtp.Header{
			Version: 2, PayloadType: pt, SequenceNumber: seq,
			Timestamp: uint32(100000 + i*960), SSRC: ssrc,
		}, Payload: Payload(ssrc, i, 32)})
	}
	return out
}
