package segment

import (
	"bytes"

	"github.com/pion/rtp"

	"mediacheck/internal/fault"
)

// CheckRTP packetizes payload into MTU-sized RTP packets and verifies
// every packet boundary: marshal/unmarshal round-trip must preserve
// SSRC, timestamp, payload type, payload bytes, and the sequence
// numbers must increment by exactly one. Missing media is never
// fabricated: the packet count follows the payload exactly.
func CheckRTP(payload []byte, pt uint8, ssrc, ts uint32, seq uint16, mtu int) (int, error) {
	if len(payload) == 0 {
		return 0, fault.New(fault.ErrRTPBoundary, 0, "empty payload cannot be packetized")
	}
	if mtu <= 0 {
		return 0, fault.New(fault.ErrRTPBoundary, 0, "mtu must be positive")
	}
	n := 0
	for off := 0; off < len(payload); off += mtu {
		end := off + mtu
		if end > len(payload) {
			end = len(payload)
		}
		wantSeq := seq + uint16(n)
		pkt := rtp.Packet{Header: rtp.Header{
			Version: 2, PayloadType: pt, SequenceNumber: wantSeq,
			Timestamp: ts, SSRC: ssrc,
		}, Payload: payload[off:end]}
		raw, err := pkt.Marshal()
		if err != nil {
			return 0, fault.New(fault.ErrRTPBoundary, int64(off), "marshal: %v", err)
		}
		var got rtp.Packet
		if err := got.Unmarshal(raw); err != nil {
			return 0, fault.New(fault.ErrRTPBoundary, int64(off), "unmarshal: %v", err)
		}
		switch {
		case got.SequenceNumber != wantSeq:
			return 0, fault.New(fault.ErrRTPBoundary, int64(off),
				"sequence gap: packet %d has seq %d, want %d", n, got.SequenceNumber, wantSeq)
		case got.SSRC != ssrc || got.Timestamp != ts || got.PayloadType != pt:
			return 0, fault.New(fault.ErrRTPBoundary, int64(off), "header fields changed across boundary")
		case !bytes.Equal(got.Payload, payload[off:end]):
			return 0, fault.New(fault.ErrRTPBoundary, int64(off), "payload changed across boundary")
		}
		n++
	}
	return n, nil
}
