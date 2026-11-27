package index

import (
	"errors"

	"github.com/pion/rtp"
)

var errPacketBeyondBuffer = errors.New("index: RTP packet extends beyond container")

// rtpPacket is a thin wrapper around pion/rtp that reports the exact number of
// bytes consumed for one packet, which the index uses to advance the mdat
// cursor and to detect a truncated trailing packet.
type rtpPacket struct {
	packet  rtp.Packet
	payload []byte
}

// consume parses one RTP packet from the front of buf. Unmarshal rejects a
// buffer that cannot hold the full declared packet, which is exactly the
// failure mode of a truncated container.
func (p *rtpPacket) consume(buf []byte) (int, error) {
	if err := p.packet.Unmarshal(buf); err != nil {
		return 0, err
	}
	n := p.packet.MarshalSize()
	if n > len(buf) {
		return 0, errPacketBeyondBuffer
	}
	p.payload = append([]byte(nil), p.packet.Payload...)
	return n, nil
}
