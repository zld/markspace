package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func TestParsePacketTimestamp(t *testing.T) {
	src := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: 7,
			Timestamp:      0x9ABCDEF0,
			SSRC:           42,
		},
		Payload: []byte{1, 2, 3, 4},
	}
	buf, err := src.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	p, ts, err := ParsePacket(buf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ts != 0x9ABCDEF0 || p.SSRC != 42 || len(p.Payload) != 4 {
		t.Fatalf("parsed packet wrong: ts=%x ssrc=%d payload=%v", ts, p.SSRC, p.Payload)
	}
}

func TestParsePacketRejectsGarbage(t *testing.T) {
	if _, _, err := ParsePacket([]byte{0x00, 0x01}); err == nil {
		t.Fatal("malformed RTP must be rejected")
	}
}

func TestUnwrapOverRealRTPStream(t *testing.T) {
	// Simulate a 90 kHz stream whose timestamps cross the 32-bit boundary.
	u := &Unwrapper{}
	var last uint64
	for i := uint32(0); i < 5; i++ {
		ts := uint32(0xFFFFFFF0) + i*10 // wraps after the first packet
		pkt := &rtp.Packet{Header: rtp.Header{Version: 2, Timestamp: ts}}
		buf, err := pkt.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		_, raw, err := ParsePacket(buf)
		if err != nil {
			t.Fatal(err)
		}
		got, uerr := u.Unwrap(raw)
		if uerr != nil {
			t.Fatalf("wrap #%d rejected: %v", i, uerr)
		}
		if i > 0 && got < last {
			t.Fatalf("stream not monotonic at %d: %d < %d", i, got, last)
		}
		last = got
	}
	if u.WrapCount() != 1 {
		t.Fatalf("expected exactly one wrap, got %d", u.WrapCount())
	}
}
