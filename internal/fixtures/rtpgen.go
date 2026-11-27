package fixtures

import (
	"bytes"
	"encoding/binary"

	pionrtp "github.com/pion/rtp"
)

// RTPPacket describes one datagram in a generated capture.
type RTPPacket struct {
	SSRC    uint32
	Seq     uint16
	TS      uint32
	Payload string
}

// Capture frames datagrams in the RTPJ container used by internal/rtp.
func Capture(packets []RTPPacket) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("RTPJ")
	for _, p := range packets {
		hdr := pionrtp.Packet{
			Header: pionrtp.Header{
				Version: 2, PayloadType: 96,
				SequenceNumber: p.Seq, Timestamp: p.TS, SSRC: p.SSRC,
			},
			Payload: []byte(p.Payload),
		}
		raw, err := hdr.Marshal()
		if err != nil {
			return nil, err
		}
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(raw)))
		b.Write(l[:])
		b.Write(raw)
	}
	return b.Bytes(), nil
}

// TruncateLast cuts n bytes off the final datagram while leaving its length
// prefix intact, which is exactly a torn capture.
func TruncateLast(capture []byte, n int) []byte {
	if n >= len(capture) {
		return nil
	}
	return capture[:len(capture)-n]
}

// MediaPackets is the healthy media stream used by the self-test: a 90 kHz
// clock, one packet per second, five packets covering the review window.
func MediaPackets(count int) []RTPPacket {
	out := make([]RTPPacket, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, RTPPacket{
			SSRC: 0x51c0ffee, Seq: uint16(1000 + i), TS: uint32(i * 90000),
			Payload: "media-sample",
		})
	}
	return out
}

// PlaybackPackets describes a capture with exactly one duplicate, one late
// (reordered) packet and one gap, mirroring the failure mode the review is
// meant to expose.
func PlaybackPackets() []RTPPacket {
	return []RTPPacket{
		{SSRC: 0x51c0ffee, Seq: 1000, TS: 0, Payload: "a"},
		{SSRC: 0x51c0ffee, Seq: 1001, TS: 90000, Payload: "b"},
		{SSRC: 0x51c0ffee, Seq: 1003, TS: 270000, Payload: "d"}, // 1002 missing
		{SSRC: 0x51c0ffee, Seq: 1001, TS: 90000, Payload: "b"},  // duplicate
		{SSRC: 0x51c0ffee, Seq: 1002, TS: 180000, Payload: "c"}, // late fill
		{SSRC: 0x51c0ffee, Seq: 1006, TS: 540000, Payload: "g"}, // 1004,1005 missing
	}
}
