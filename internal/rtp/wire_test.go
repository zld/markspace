package rtp

import (
	"errors"
	"testing"

	pionrtp "github.com/pion/rtp"
)

func TestWireRoundTrip(t *testing.T) {
	p := Packet{Seq: 65535, TS: 0xFFFFFFFF, SSRC: WireSSRC, Payload: []byte("wire fragment")}
	b, err := ToWire(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromWire(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != p.Seq || got.TS != p.TS || got.SSRC != p.SSRC || string(got.Payload) != string(p.Payload) {
		t.Fatalf("wire roundtrip %+v", got)
	}
}

func TestWireRejectsNonProfile(t *testing.T) {
	if _, err := FromWire(nil); !errors.Is(err, ErrWireFormat) {
		t.Fatalf("empty accepted: %v", err)
	}
	if _, err := FromWire([]byte{0x00}); !errors.Is(err, ErrWireFormat) {
		t.Fatalf("truncated accepted: %v", err)
	}
	if _, err := ToWire(Packet{Seq: 1}); !errors.Is(err, ErrWireFormat) {
		t.Fatalf("empty payload marshalled")
	}
	good, err := ToWire(Packet{Seq: 1, Payload: []byte{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), good...)
	bad[1] = (bad[1] & 0xfc) | 97 // payload type 97
	if _, err := FromWire(bad); !errors.Is(err, ErrWireFormat) {
		t.Fatalf("foreign payload type accepted")
	}
}

func TestWireMatchesPionHeaderFields(t *testing.T) {
	for i := 0; i < 200; i++ {
		payload := make([]byte, 1+(i*37)%300)
		for j := range payload {
			payload[j] = byte(i + j)
		}
		p := Packet{Seq: uint16(i * 977), TS: uint32(i) * 0x9e3779b9, SSRC: WireSSRC, Payload: payload}
		wire, err := ToWire(p)
		if err != nil {
			t.Fatal(err)
		}
		var pp pionrtp.Packet
		if err := pp.Unmarshal(wire); err != nil {
			t.Fatal(err)
		}
		if pp.SequenceNumber != p.Seq || pp.Timestamp != p.TS || pp.SSRC != p.SSRC || pp.Version != 2 {
			t.Fatalf("iteration %d header mismatch: %+v", i, pp.Header)
		}
		if len(pp.Payload) != len(payload) {
			t.Fatalf("iteration %d payload length %d", i, len(pp.Payload))
		}
		back, err := FromWire(wire)
		if err != nil {
			t.Fatal(err)
		}
		if string(back.Payload) != string(payload) {
			t.Fatalf("iteration %d payload changed", i)
		}
	}
}
