package protocol

import (
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := Packet{StreamID: 7, Seq: 0xFFFFFFFE, SendTimeMs: 123456789, Type: FrameAudio, Payload: []byte{1, 2, 3, 4}}
	out, err := Decode(Encode(in), 0)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out.StreamID != in.StreamID || out.Seq != in.Seq || out.SendTimeMs != in.SendTimeMs || out.Type != in.Type {
		t.Fatalf("header mismatch: %+v", out)
	}
	if len(out.Payload) != 4 || out.Payload[3] != 4 {
		t.Fatalf("payload mismatch: %v", out.Payload)
	}
}

func TestDecodeRejects(t *testing.T) {
	good := Encode(Packet{StreamID: 1, Seq: 1, SendTimeMs: 1, Type: FrameAudio, Payload: []byte{9}})

	if _, err := Decode(good[:HeaderSize-1], 0); !errors.Is(err, ErrTruncated) {
		t.Fatalf("truncated: %v", err)
	}
	bad := append([]byte(nil), good...)
	bad[0] = 0xFF
	if _, err := Decode(bad, 0); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("magic: %v", err)
	}
	bad = append([]byte(nil), good...)
	bad[18] = 99
	if _, err := Decode(bad, 0); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("type: %v", err)
	}
	// Declared length disagrees with datagram size.
	bad = append([]byte(nil), good...)
	bad[20] = 5
	if _, err := Decode(bad, 0); !errors.Is(err, ErrBadLength) {
		t.Fatalf("length: %v", err)
	}
	// Payload larger than caller-imposed limit.
	if _, err := Decode(good, 0); err != nil {
		t.Fatalf("limit ok: %v", err)
	}
	if _, err := Decode(good, 0); err != nil {
		t.Fatal(err)
	}
	big := Encode(Packet{StreamID: 1, Seq: 1, SendTimeMs: 1, Type: FrameAudio, Payload: make([]byte, 100)})
	if _, err := Decode(big, 50); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
}

func TestDecodeCopiesPayload(t *testing.T) {
	buf := Encode(Packet{StreamID: 1, Seq: 1, SendTimeMs: 1, Type: FrameAudio, Payload: []byte{7}})
	p, err := Decode(buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	buf[HeaderSize] = 0
	if p.Payload[0] != 7 {
		t.Fatal("payload aliases caller buffer")
	}
}
