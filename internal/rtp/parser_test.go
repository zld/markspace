package rtp

import (
	"errors"
	"testing"
)

func buildPacket(b0, b1 byte, seq uint16, ts, ssrc uint32, body []byte) []byte {
	buf := make([]byte, 12+len(body))
	buf[0], buf[1] = b0, b1
	buf[2], buf[3] = byte(seq>>8), byte(seq)
	for i := 0; i < 4; i++ {
		buf[4+i] = byte(ts >> (24 - 8*i))
		buf[8+i] = byte(ssrc >> (24 - 8*i))
	}
	copy(buf[12:], body)
	return buf
}

func TestParseBasicAndCSRC(t *testing.T) {
	p := NewParser(1500)
	buf := buildPacket(0x80, 0x80|96, 0x0102, 0x03040506, 0x0708090a,
		[]byte{1, 2, 3})
	pkt, err := p.Parse(buf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pkt.Header.SequenceNumber != 0x0102 || pkt.Header.Timestamp != 0x03040506 ||
		pkt.Header.SSRC != 0x0708090a || !pkt.Header.Marker ||
		pkt.Header.PayloadType != 96 || len(pkt.Payload) != 3 {
		t.Fatalf("unexpected packet: %+v", pkt)
	}

	// CSRC count 2.
	csrc := []byte{0, 0, 0, 1, 0, 0, 0, 2, 0xaa}
	buf = buildPacket(0x80|2, 96, 1, 10, 20, csrc)
	pkt, err = p.Parse(buf)
	if err != nil {
		t.Fatalf("csrc parse: %v", err)
	}
	if len(pkt.Header.CSRC) != 2 || pkt.Header.CSRC[0] != 1 ||
		pkt.Header.CSRC[1] != 2 || len(pkt.Payload) != 1 || pkt.Payload[0] != 0xaa {
		t.Fatalf("bad CSRC decode: %+v", pkt.Header.CSRC)
	}
}

func TestParseExtension(t *testing.T) {
	p := NewParser(1500)
	// extension bit, 1 extension word (4 bytes) + 2 payload bytes.
	body := []byte{
		0xbe, 0xde, // profile
		0x00, 0x01, // length in words = 1
		1, 2, 3, 4, // extension payload
		9, 9, // RTP payload
	}
	buf := buildPacket(0x90, 96, 5, 6, 7, body)
	pkt, err := p.Parse(buf)
	if err != nil {
		t.Fatalf("ext parse: %v", err)
	}
	if pkt.Header.ExtensionProfile != 0xbede ||
		len(pkt.Header.ExtensionPayload) != 4 || len(pkt.Payload) != 2 {
		t.Fatalf("bad extension decode: %+v", pkt)
	}
}

func TestParsePadding(t *testing.T) {
	p := NewParser(1500)
	body := []byte{1, 2, 3, 0, 0, 0, 3} // 4 payload + 3 padding bytes, marker = 3
	buf := buildPacket(0xa0, 96, 1, 1, 1, body)
	pkt, err := p.Parse(buf)
	if err != nil {
		t.Fatalf("padding parse: %v", err)
	}
	if pkt.PaddingSize != 3 || len(pkt.Payload) != 4 {
		t.Fatalf("padding not stripped: %+v", pkt)
	}
}

func TestParseErrors(t *testing.T) {
	p := NewParser(64)
	check := func(name string, buf []byte, want error) {
		t.Helper()
		if _, err := p.Parse(buf); !errors.Is(err, want) {
			t.Errorf("%s: want %v, got %v", name, want, err)
		}
	}

	check("truncated fixed header", make([]byte, 11), ErrTruncatedHeader)
	check("bad version", buildPacket(0x00, 96, 1, 1, 1, []byte{1}), ErrBadVersion)

	// CSRC table runs past the packet end.
	check("csrc overflow", buildPacket(0x80|3, 96, 1, 1, 1, []byte{1}),
		ErrTruncatedHeader)

	// Extension declares more words than the packet contains.
	check("ext out of bounds",
		buildPacket(0x90, 96, 1, 1, 1, []byte{0, 0, 0x10, 0}),
		ErrExtOutOfBounds)

	// padding marker of zero and oversized padding.
	check("zero padding", buildPacket(0xa0, 96, 1, 1, 1, []byte{1, 0}),
		ErrIllegalPadding)
	check("huge padding", buildPacket(0xa0, 96, 1, 1, 1, []byte{1, 99}),
		ErrIllegalPadding)

	// oversized datagram.
	check("oversize", make([]byte, 65), ErrPacketTooLarge)
}
