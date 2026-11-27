package timeline

import (
	"github.com/pion/rtp"

	"mediapack/internal/model"
)

// rtpExtID is the one-byte header-extension id carrying the unwrapped clock
// reference alongside the native 32-bit timestamp.
const rtpExtID = 3

// WrapPacket builds an RTP packet whose 32-bit Timestamp holds the (possibly
// wrapped) raw value and whose header extension carries the unwrapped extended
// clock. This exercises pion/rtp marshalling and the project's wrap expansion
// on the exact bytes that would cross a real boundary.
func WrapPacket(raw uint32, extended uint64) (*rtp.Packet, error) {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: uint16(raw & 0xffff),
			Timestamp:      raw,
			SSRC:           1,
		},
		Payload: []byte{0x00},
	}
	ext := make([]byte, 8)
	putU64(ext, extended)
	if err := p.Header.SetExtension(rtpExtID, ext); err != nil {
		return nil, err
	}
	return p, nil
}

// ParseWrapPacket decodes the packet, reads the raw 32-bit timestamp back and
// returns it together with the extended clock from the header extension.
func ParseWrapPacket(buf []byte) (raw uint32, extended uint64, err error) {
	var p rtp.Packet
	if err := p.Unmarshal(buf); err != nil {
		return 0, 0, err
	}
	ext := p.Header.GetExtension(rtpExtID)
	if len(ext) < 8 {
		return p.Header.Timestamp, 0, model.NewError(model.CodeMalformedContainer,
			"missing RTP clock extension")
	}
	return p.Header.Timestamp, readU64(ext), nil
}

// ExpandRTP applies the project's deterministic 32-bit clock expansion to a
// raw timestamp given the last extended value. It returns the unwrapped value
// and whether a backward wrap was detected; the raw input is never mutated.
func ExpandRTP(prevExtended uint64, raw uint32) (extended uint64, wrapped bool) {
	return model.ExpandClock(prevExtended, uint64(raw), modelRTPModulus)
}

// modelRTPModulus mirrors fixture.RTPModulus without an import cycle.
const modelRTPModulus uint64 = 1 << 32

func putU64(b []byte, v uint64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}

func readU64(b []byte) uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v
}
