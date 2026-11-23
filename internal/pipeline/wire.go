package pipeline

import (
	"encoding/binary"

	"github.com/pion/rtp"

	"mediad/internal/model"
)

func mkPacket(seq uint16, ts, ssrc uint32, pt uint8, marker bool, payload []byte, sendMs int64) pendingPacket {
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version: 2, Marker: marker, PayloadType: pt,
			SequenceNumber: seq, Timestamp: ts, SSRC: ssrc,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	frame := append([]byte{byte(len(raw) >> 8), byte(len(raw))}, raw...)
	ev := model.Event{SSRC: ssrc, Seq16: seq, RTPTs: ts, Keyframe: marker}
	return pendingPacket{sendMs: sendMs, frame: frame, ev: ev, media: true}
}

// mkSR builds a 28-byte RTCP Sender Report mapping wallMs to rtpTs.
func mkSR(ssrc uint32, wallMs int64, rtpTs uint32) []byte {
	b := make([]byte, 28)
	b[0], b[1] = 0x80, 200
	binary.BigEndian.PutUint16(b[2:], 6)
	binary.BigEndian.PutUint32(b[4:], ssrc)
	sec := uint64(wallMs)/1000 + 2208988800
	frac := uint64(wallMs%1000) * (1 << 32) / 1000
	binary.BigEndian.PutUint32(b[8:], uint32(sec))
	binary.BigEndian.PutUint32(b[12:], uint32(frac))
	binary.BigEndian.PutUint32(b[16:], rtpTs)
	return append([]byte{0x00, 28}, b...)
}

// mkRR builds a minimal 8-byte RTCP Receiver Report.
func mkRR(mediaSSRC uint32) []byte {
	b := make([]byte, 8)
	b[0], b[1] = 0x80, 201
	binary.BigEndian.PutUint16(b[2:], 1)
	binary.BigEndian.PutUint32(b[4:], mediaSSRC)
	return append([]byte{0x00, 8}, b...)
}

// mkNACK builds a generic NACK (PT=205, fmt=1) with one PID/BLP pair.
func mkNACK(mediaSSRC uint32, pid uint16) []byte {
	b := make([]byte, 16)
	b[0], b[1] = 0x81, 205
	binary.BigEndian.PutUint16(b[2:], 3)
	binary.BigEndian.PutUint32(b[4:], 1) // feedback sender SSRC
	binary.BigEndian.PutUint32(b[8:], mediaSSRC)
	binary.BigEndian.PutUint16(b[12:], pid)
	return append([]byte{0x00, 16}, b...)
}

// corruptExtension sets the X bit and plants an extension header whose
// declared length exceeds the packet, inside the frame's length prefix.
func corruptExtension(frame []byte) []byte {
	body := append([]byte(nil), frame[2:]...)
	if len(body) < 12 {
		return frame
	}
	body[0] |= 0x10                       // X bit
	ext := []byte{0x00, 0x00, 0xFF, 0xFF} // profile + absurd length
	out := append(body[:12:12], ext...)
	out = append(out, body[12:]...)
	return append([]byte{byte(len(out) >> 8), byte(len(out))}, out...)
}
