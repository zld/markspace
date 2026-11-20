package tests

import (
	"encoding/binary"
	"testing"

	"jitterlab/internal/packet"
)

func TestPacketWireRoundTrip(t *testing.T) {
	p, err := packet.New(0xffee, 42, 123456, 2, true, []byte("voice"))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := packet.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != packet.HeaderLen+5 {
		t.Fatalf("wire length = %d", len(wire))
	}
	got, err := packet.Unmarshal(wire)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Header != p.Header || !got.EndOfFrame() {
		t.Fatalf("header mismatch: %+v vs %+v", got.Header, p.Header)
	}
	if string(got.Payload) != "voice" {
		t.Fatalf("payload = %q", got.Payload)
	}
	if binary.BigEndian.Uint16(wire[14:16]) == 0 {
		t.Fatal("checksum not stored")
	}
}
