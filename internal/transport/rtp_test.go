package transport

import (
	"testing"

	"tsrepair/internal/mediaerr"
	"tsrepair/internal/ts"
)

func TestEncodeDecodeRoundtrip(t *testing.T) {
	enc := NewEncoder(0xDEADBEEF, 33)
	tsPkt := make([]byte, ts.PacketSize)
	tsPkt[0] = ts.SyncByte
	wire, err := enc.EncodePacket(tsPkt, true, 90000)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.SSRC != 0xDEADBEEF || got.PayloadType != 33 || !got.Marker {
		t.Fatalf("header mismatch: %+v", got.Header)
	}
	if got.SequenceNumber != 0 || len(got.Payload) != ts.PacketSize || got.Payload[0] != ts.SyncByte {
		t.Fatalf("payload/seq mismatch: %+v", got)
	}
	// Second packet gets the next sequence number.
	wire2, _ := enc.EncodePacket(tsPkt, false, 90000)
	got2, _ := Decode(wire2)
	if got2.SequenceNumber != 1 {
		t.Errorf("seq=%d want 1", got2.SequenceNumber)
	}
}

func TestEncodeRejectsBadPayload(t *testing.T) {
	enc := NewEncoder(1, 33)
	for _, n := range []int{0, 100, 8 * 188} {
		if _, err := enc.EncodePacket(make([]byte, n), false, 0); err == nil {
			t.Errorf("payload size %d must be rejected", n)
		}
	}
}

func TestSeqCheckerGapRepeatJump(t *testing.T) {
	ec := &mediaerr.Collector{}
	c := NewSeqChecker(1, ec)
	for _, seq := range []uint16{10, 12, 12, 5} {
		c.Observe(seq, int64(seq)*188)
	}
	if ec.CountByCode(mediaerr.CodeRTPSeqGap) != 1 {
		t.Error("missing gap event")
	}
	if ec.CountByCode(mediaerr.CodeRTPSeqRepeat) != 1 {
		t.Error("missing repeat event")
	}
	if ec.CountByCode(mediaerr.CodeRTPSeqJump) != 1 {
		t.Error("missing jump event")
	}
	for _, e := range ec.All() {
		if e.Offset == 0 {
			t.Error("offset must be recorded")
		}
	}
}

func TestSeqCheckerContinuousNoErrors(t *testing.T) {
	ec := &mediaerr.Collector{}
	c := NewSeqChecker(1, ec)
	for seq := uint16(0); seq < 100; seq++ {
		c.Observe(seq, 0)
	}
	if len(ec.All()) != 0 {
		t.Fatalf("unexpected errors: %v", ec.All())
	}
}

func TestSeqChecker16BitWrap(t *testing.T) {
	ec := &mediaerr.Collector{}
	c := NewSeqChecker(1, ec)
	c.Observe(0xFFFF, 0)
	c.Observe(0x0000, 188) // natural wrap, delta must be 1
	if len(ec.All()) != 0 {
		t.Fatalf("wrap must stay continuous: %v", ec.All())
	}
}
