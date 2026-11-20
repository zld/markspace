package wire

import (
	"bytes"
	"errors"
	"testing"
)

func samplePacket() Packet {
	payload := make([]byte, 1920)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	return Packet{Seq: 65530, Timestamp: 0xFFFFFF00, Payload: payload}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	want := samplePacket()
	buf, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(buf) != HeaderSize+len(want.Payload) {
		t.Fatalf("encoded length %d", len(buf))
	}
	got, err := Decode(buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Seq != want.Seq || got.Timestamp != want.Timestamp {
		t.Fatalf("header mismatch: %+v", got)
	}
	if !bytes.Equal(got.Payload, want.Payload) {
		t.Fatal("payload mismatch")
	}
}

func TestDecodeRejectsCorruption(t *testing.T) {
	buf, err := Encode(samplePacket())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func([]byte) []byte{
		"short":    func(b []byte) []byte { return b[:HeaderSize-1] },
		"magic":    func(b []byte) []byte { b[0] ^= 0xFF; return b },
		"version":  func(b []byte) []byte { b[2] = 99; return b },
		"checksum": func(b []byte) []byte { b[HeaderSize+10] ^= 0x01; return b },
		"truncated": func(b []byte) []byte {
			return b[:len(b)-3]
		},
	}
	for name, mutate := range cases {
		raw, _ := Encode(samplePacket())
		if _, err := Decode(mutate(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
		_ = buf
	}
}

func TestDecodeRejectsOversizedPayloadField(t *testing.T) {
	raw, _ := Encode(samplePacket())
	// Force payload length field beyond MaxPayload.
	raw[10] = 0xFF
	raw[11] = 0xFF
	if _, err := Decode(raw); !errors.Is(err, ErrBadLength) {
		t.Fatalf("want ErrBadLength, got %v", err)
	}
}

func TestEncodeRejectsOversizedPayload(t *testing.T) {
	p := Packet{Seq: 1, Payload: make([]byte, MaxPayload+1)}
	if _, err := Encode(p); !errors.Is(err, ErrPayloadLarge) {
		t.Fatalf("want ErrPayloadLarge, got %v", err)
	}
}

func TestEmptyPayloadRoundTrip(t *testing.T) {
	buf, err := Encode(Packet{Seq: 7, Timestamp: 9})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Decode(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Payload) != 0 {
		t.Fatalf("payload len %d", len(p.Payload))
	}
}
