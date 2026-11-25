package clock

import (
	"github.com/pion/rtp"
)

// payloadType96 is the dynamic RTP payload type used for all synthetic
// fixture streams; it has no static binding (RFC 3551 §3).
const payloadType96 = 96

// RTPPacket wraps one frame's payload in an RTP packet whose sequence
// number and timestamp derive from the shared timeline, reusing the
// github.com/pion/rtp marshalling components.
func (t *TimeBase) RTPPacket(frame uint64, payload []byte) (*rtp.Packet, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    payloadType96,
			SequenceNumber: uint16(frame), // deterministic per frame
			Timestamp:      t.NSToRTP(t.FrameToNS(frame)),
			SSRC:           0x4d454441, // "MEDA"
		},
		Payload: append([]byte(nil), payload...),
	}
	return pkt, nil
}

// MarshalRTP returns the wire bytes of the RTP packet for frame.
func (t *TimeBase) MarshalRTP(frame uint64, payload []byte) ([]byte, error) {
	pkt, err := t.RTPPacket(frame, payload)
	if err != nil {
		return nil, err
	}
	return pkt.Marshal()
}

// UnmarshalRTP parses wire bytes back into sequence number, timestamp and
// payload. It is the inverse used by round-trip checks.
func UnmarshalRTP(buf []byte) (seq uint16, ts uint32, payload []byte, err error) {
	var pkt rtp.Packet
	if err = pkt.Unmarshal(buf); err != nil {
		return 0, 0, nil, err
	}
	return pkt.SequenceNumber, pkt.Timestamp, append([]byte(nil), pkt.Payload...), nil
}
