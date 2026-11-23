package transport

import (
	"testing"

	"archivecheck/internal/model"
)

func TestPionRoundTrip(t *testing.T) {
	in := Packet{Sequence: 65533, Timestamp: 123456789, Payload: []byte("frame-bytes"), Marker: true}
	raw, err := MarshalRTP(in, RTPSSRC)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 12 {
		t.Fatalf("RTP packet too short: %d bytes", len(raw))
	}
	out, fault := ParseRTP(raw, Source{Segment: "s1"})
	if fault != nil {
		t.Fatalf("parse: %v", fault)
	}
	if out.Sequence != in.Sequence || out.Timestamp != in.Timestamp || out.Marker != in.Marker {
		t.Fatalf("round trip header mismatch: %+v", out)
	}
	if string(out.Payload) != string(in.Payload) {
		t.Fatalf("payload mismatch %q", out.Payload)
	}
}

func TestPionRejectsGarbage(t *testing.T) {
	for _, raw := range [][]byte{
		{},
		{0x80},
		{0x80, 0x60, 0x00, 0x01}, // header truncated
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	} {
		if _, fault := ParseRTP(raw, Source{Segment: "s9"}); fault == nil {
			t.Fatalf("garbage %x should be rejected", raw)
		} else if fault.Code != model.CodeTransportBadPayload || fault.Source.Segment != "s9" {
			t.Fatalf("fault %+v", fault)
		}
	}
}
