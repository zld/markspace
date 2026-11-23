package rtp

import (
	"github.com/pion/rtp"
)

// Packet is the minimal RTP view the pipeline consumes. The pion type is
// kept behind this alias so callers do not depend on the third-party API
// directly.
type Packet = rtp.Packet

// ParsePacket decodes one RTP packet and returns its 32-bit timestamp.
// Malformed packets produce an error from the pion unmarshaller.
func ParsePacket(buf []byte) (*Packet, uint32, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(buf); err != nil {
		return nil, 0, err
	}
	return p, p.Timestamp, nil
}

// ClockRate90kHz is the RTP clock used by most video payloads and by the
// CMAF timing checks in this library.
const ClockRate90kHz uint32 = 90000
