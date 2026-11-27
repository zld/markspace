package index

import (
	"encoding/binary"
	"errors"

	"github.com/pion/rtp"
)

var (
	errPacketBeyondBuffer = errors.New("index: RTP packet extends beyond container")
	errNoLengthPrefix     = errors.New("index: truncated RTP length prefix")
)

// rtpPacket is a thin wrapper around pion/rtp that understands the 2-byte
// big-endian length prefix the container places before each packet. RTP
// headers carry no length field, so without the prefix a packet cannot be
// delimited inside an mdat box.
type rtpPacket struct {
	packet  rtp.Packet
	payload []byte
}

// consume parses one framed RTP packet from the front of buf and returns the
// total bytes consumed (prefix + packet). A short prefix or a declared length
// running past the buffer is reported as an error, which is exactly the
// failure mode of a truncated container.
func (p *rtpPacket) consume(buf []byte) (int, error) {
	if len(buf) < 2 {
		return 0, errNoLengthPrefix
	}
	pktLen := int(binary.BigEndian.Uint16(buf[:2]))
	if 2+pktLen > len(buf) {
		return 0, errPacketBeyondBuffer
	}
	raw := buf[2 : 2+pktLen]
	if err := p.packet.Unmarshal(raw); err != nil {
		return 0, err
	}
	p.payload = append([]byte(nil), p.packet.Payload...)
	return 2 + pktLen, nil
}
