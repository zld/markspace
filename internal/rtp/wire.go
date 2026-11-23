// Package rtp models packet loss and reordering for the fan-in stage using
// github.com/pion/rtp for wire-format parsing. This file converts between wire
// packets and the recovery stage's packet view.
package rtp

import (
	"fmt"

	"github.com/pion/rtp"

	"mediad/internal/merr"
)

// BuildPacket encodes a media block as an RTP packet with the given sequence
// number, so fixtures can round-trip through pion's parser.
func BuildPacket(seq uint16, payload []byte, marker bool) ([]byte, error) {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      uint32(seq) * 3000,
			SSRC:           0x4D454449,
			Marker:         marker,
		},
		Payload: payload,
	}
	return p.Marshal()
}

// ParsePacket decodes one wire packet into the fields recovery consumes.
func ParsePacket(buf []byte) (Packet, error) {
	var p rtp.Packet
	if err := p.Unmarshal(buf); err != nil {
		return Packet{}, merr.Wrap(err, "rtp.parse", merr.CodeBadInput, -1)
	}
	return Packet{Sequence: p.SequenceNumber, PayloadLen: len(p.Payload), Marker: p.Marker}, nil
}

// ParseAll decodes a batch of wire packets in arrival order.
func ParseAll(batch [][]byte) ([]Packet, error) {
	out := make([]Packet, 0, len(batch))
	for i, buf := range batch {
		p, err := ParsePacket(buf)
		if err != nil {
			return out, merr.New("rtp.parseAll", merr.CodeBadInput, int64(i),
				"packet %d: %v", i, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Recover runs a batch of arrival-ordered wire packets through a fresh cursor.
func Recover(batch [][]byte, window int) ([]Packet, Stats, error) {
	pkts, err := ParseAll(batch)
	if err != nil {
		return nil, Stats{}, err
	}
	c := NewCursor(window)
	var out []Packet
	for _, p := range pkts {
		out = c.Push(p, out)
	}
	out = c.Flush(out)
	return out, c.Stats(), nil
}

// RecoverPackets is Recover for already-decoded packets.
func RecoverPackets(pkts []Packet, window int) ([]Packet, Stats) {
	c := NewCursor(window)
	var out []Packet
	for _, p := range pkts {
		out = c.Push(p, out)
	}
	out = c.Flush(out)
	return out, c.Stats()
}

// String renders stats for logs.
func (s Stats) String() string {
	return fmt.Sprintf("delivered=%d recovered=%d lost=%d dup=%d maxreorder=%d rate=%.3f",
		s.Delivered, s.Recovered, s.Lost, s.Duplicate, s.MaxReorder, s.PacketRecoveryRate())
}
