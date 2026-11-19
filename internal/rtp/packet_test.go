package rtp

import (
	"encoding/binary"
	"errors"
	"testing"
)

func buildPacket(t *testing.T, opts func(*[]byte)) []byte {
	t.Helper()
	buf := make([]byte, HeaderSize(1, 1, true)+8+4) // 1 CSRC + ext(1 word) + 8 payload + 4 padding
	buf[0] = (2 << 6) | 0x10 | 0x20 | 1 // V2, ext, padding, 1 CSRC
	buf[1] = 0x80 | 96                  // marker, PT 96
	binary.BigEndian.PutUint16(buf[2:4], 0x1234)
	binary.BigEndian.PutUint32(buf[4:8], 0xAABBCCDD)
	binary.BigEndian.PutUint32(buf[8:12], 0x11223344)
	binary.BigEndian.PutUint32(buf[12:16], 0x55667788) // CSRC
	binary.BigEndian.PutUint16(buf[16:18], 0xBEDE)    // extension profile
	binary.BigEndian.PutUint16(buf[18:20], 1)         // 1 extension word
	binary.BigEndian.PutUint32(buf[20:24], 0x01020304)
	for i := 24; i < 32; i++ {
		buf[i] = byte(i)
	}
	buf[len(buf)-1] = 4 // padding size
	if opts != nil {
		opts(&buf)
	}
	return buf
}

func TestParseValidPacket(t *testing.T) {
	p, err := Parse(buildPacket(t, nil))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.SequenceNumber != 0x1234 || p.Timestamp != 0xAABBCCDD || p.SSRC != 0x11223344 {
		t.Fatalf("fixed header mismatch: %+v", p)
	}
	if !p.Marker || p.PayloadType != 96 || !p.Padding || !p.Extension || p.CSRCCount != 1 {
		t.Fatalf("flags mismatch: %+v", p)
	}
	if len(p.CSRC) != 1 || p.CSRC[0] != 0x55667788 {
		t.Fatalf("CSRC mismatch: %+v", p.CSRC)
	}
	if p.ExtensionProfile != 0xBEDE || len(p.ExtensionPayload) != 4 {
		t.Fatalf("extension mismatch: %+v", p.ExtensionPayload)
	}
	if len(p.Payload) != 8 || p.PaddingSize != 4 {
		t.Fatalf("payload/padding mismatch: payload=%d pad=%d", len(p.Payload), p.PaddingSize)
	}
}

func TestParseMalformedInputs(t *testing.T) {
	cases := map[string]func([]byte) []byte{
		"empty":          func(b []byte) []byte { return nil },
		"too short":      func(b []byte) []byte { return b[:4] },
		"bad version":    func(b []byte) []byte { b[0] &^= 0xC0; return b },
		"csrc overflow":  func(b []byte) []byte { b[0] = (b[0] & 0xf0) | 15; return b },
		"ext overflow":   func(b []byte) []byte { binary.BigEndian.PutUint16(b[18:20], 100); return b },
		"padding too big": func(b []byte) []byte { b[len(b)-1] = 200; return b },
		"zero padding":   func(b []byte) []byte { b[len(b)-1] = 0; return b },
		"oversize": func(b []byte) []byte {
			out := make([]byte, MaxPacketSize+1)
			out[0] = 0x80
			return out
		},
	}
	want := map[string]error{
		"empty":           ErrShortPacket,
		"too short":       ErrShortPacket,
		"bad version":     ErrBadVersion,
		"csrc overflow":   ErrCSRCOverflow,
		"ext overflow":    ErrExtensionOverflow,
		"padding too big": ErrPaddingOverflow,
		"zero padding":    ErrZeroPadding,
		"oversize":        ErrPacketTooLarge,
	}
	for name, mutate := range cases {
		buf := buildPacket(t, nil)
		buf = mutate(buf)
		p, err := Parse(buf)
		if err == nil {
			t.Fatalf("%s: expected error, got packet %+v", name, p)
		}
		if !errors.Is(err, want[name]) {
			t.Fatalf("%s: got %v, want %v", name, err, want[name])
		}
	}
}

func TestParseNoExtensionNoPadding(t *testing.T) {
	buf := make([]byte, fixedHeaderSize+5)
	buf[0] = 2 << 6
	binary.BigEndian.PutUint16(buf[2:4], 7)
	p, err := Parse(buf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Extension || p.Padding || p.CSRCCount != 0 || len(p.Payload) != 5 {
		t.Fatalf("unexpected fields: %+v", p)
	}
}
