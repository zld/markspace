package rtp

import (
	"errors"
	"testing"

	pionrtp "github.com/pion/rtp"

	"rtp-rebuild/internal/fault"
)

const testSSRC = 0xDEADBEEF

func TestInOrderStream(t *testing.T) {
	r := NewReorderer(8)
	for i := 0; i < 100; i++ {
		r.Accept(testSSRC, uint16(1000+i), uint32(i*3000), i%10 == 9, 96, int64(i))
	}
	res := r.Finish()
	if len(res.Packets) != 100 {
		t.Fatalf("packets = %d, want 100", len(res.Packets))
	}
	if res.Duplicates != 0 || res.LossesTotal() != 0 {
		t.Fatalf("duplicates=%d loss=%d, want 0/0", res.Duplicates, res.LossesTotal())
	}
	for i, p := range res.Packets {
		if p.ExtSeq != int64(1000+i) {
			t.Fatalf("packet %d ext = %d", i, p.ExtSeq)
		}
		if p.TS != 0 && i > 0 && p.TS < res.Packets[i-1].TS {
			t.Fatalf("timestamps not monotonic at %d", i)
		}
	}
}

func TestOutOfOrderWithinWindow(t *testing.T) {
	r := NewReorderer(16)
	order := []uint16{5, 3, 1, 2, 4, 0}
	for _, seq := range order {
		r.Accept(testSSRC, seq, uint32(seq)*3000, false, 96, int64(seq))
	}
	res := r.Finish()
	if len(res.Packets) != 6 {
		t.Fatalf("packets = %d, want 6", len(res.Packets))
	}
	if res.LossesTotal() != 0 {
		t.Fatalf("loss = %d, want 0", res.LossesTotal())
	}
	for i, p := range res.Packets {
		if p.Seq != uint16(i) {
			t.Fatalf("packet %d seq = %d, want %d", i, p.Seq, i)
		}
	}
}

func TestDuplicateCountedOnce(t *testing.T) {
	r := NewReorderer(8)
	for i := 0; i < 10; i++ {
		r.Accept(testSSRC, uint16(i), uint32(i*3000), false, 96, int64(i))
		r.Accept(testSSRC, uint16(i), uint32(i*3000), false, 96, int64(i)) // duplicate
	}
	res := r.Finish()
	if len(res.Packets) != 10 {
		t.Fatalf("packets = %d, want 10", len(res.Packets))
	}
	if res.Duplicates != 10 {
		t.Fatalf("duplicates = %d, want 10", res.Duplicates)
	}
	if res.LossesTotal() != 0 {
		t.Fatalf("loss = %d, want 0", res.LossesTotal())
	}
}

func TestWindowExhaustionDeclaresLoss(t *testing.T) {
	r := NewReorderer(4)
	for i := 0; i < 20; i++ {
		if i == 5 || i == 6 {
			continue // drop two packets
		}
		r.Accept(testSSRC, uint16(i), uint32(i*3000), false, 96, int64(i))
	}
	res := r.Finish()
	if res.LossesTotal() != 2 {
		t.Fatalf("loss = %d, want 2", res.LossesTotal())
	}
	if len(res.Losses) != 1 {
		t.Fatalf("loss runs = %d, want 1", len(res.Losses))
	}
	if res.Losses[0].First != 5 || res.Losses[0].Last != 6 {
		t.Fatalf("loss = %+v", res.Losses[0])
	}
	if len(res.Packets) != 18 {
		t.Fatalf("packets = %d, want 18", len(res.Packets))
	}
}

func TestLatePacketAfterLossIsAnomaly(t *testing.T) {
	r := NewReorderer(2)
	for i := 0; i < 6; i++ {
		if i == 3 {
			continue
		}
		r.Accept(testSSRC, uint16(i), uint32(i*3000), false, 96, int64(i))
	}
	// Sequence 3 arrives after the window closed on it.
	r.Accept(testSSRC, 3, 9000, false, 96, 99)
	res := r.Finish()
	if res.LossesTotal() != 1 {
		t.Fatalf("loss = %d, want 1", res.LossesTotal())
	}
	if len(res.Anomalies) != 1 || res.Anomalies[0].Seq != 3 {
		t.Fatalf("anomalies = %+v", res.Anomalies)
	}
	for _, p := range res.Packets {
		if p.Seq == 3 {
			t.Fatal("late packet must not be fabricated into the timeline")
		}
	}
}

func TestSequenceWraparound(t *testing.T) {
	r := NewReorderer(8)
	start := 65530
	for i := 0; i < 20; i++ {
		seq := uint16(start + i)
		r.Accept(testSSRC, seq, uint32(0xFFFF0000+uint32(i*30000)), false, 96, int64(i))
	}
	res := r.Finish()
	if len(res.Packets) != 20 {
		t.Fatalf("packets = %d, want 20", len(res.Packets))
	}
	if res.LossesTotal() != 0 {
		t.Fatalf("loss = %d, want 0", res.LossesTotal())
	}
	if res.Packets[0].ExtSeq != int64(65530) || res.Packets[19].ExtSeq != int64(65530+19) {
		t.Fatalf("ext seq range = %d..%d", res.Packets[0].ExtSeq, res.Packets[19].ExtSeq)
	}
	if res.TimestampWraps != 1 {
		t.Fatalf("timestamp wraps = %d, want 1", res.TimestampWraps)
	}
	if res.Packets[19].TS <= res.Packets[18].TS {
		t.Fatalf("timestamps should keep increasing across the 32 bit wrap")
	}
}

func TestParseErrorsCarryOffset(t *testing.T) {
	r := NewReorderer(8)
	err := r.Push([]byte{1, 2, 3}, 77)
	if !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("error = %v, want truncated", err)
	}
	if off, _ := fault.OffsetOf(err); off != 77 {
		t.Fatalf("offset = %d, want 77", off)
	}

	bad := make([]byte, 12)
	bad[0] = 0x40 // version 1
	err = r.Push(bad, 12)
	if !errors.Is(err, fault.ErrUnsupported) {
		t.Fatalf("bad version error = %v", err)
	}
}

func TestPushWirePacket(t *testing.T) {
	r := NewReorderer(8)
	for i := 0; i < 5; i++ {
		pkt := &pionrtp.Packet{
			Header: pionrtp.Header{
				Version: 2, SequenceNumber: uint16(i), Timestamp: uint32(i * 3000),
				SSRC: testSSRC, PayloadType: 96,
			},
			Payload: []byte("media"),
		}
		raw, err := pkt.Marshal()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := r.Push(raw, int64(i)); err != nil {
			t.Fatalf("push: %v", err)
		}
	}
	res := r.Finish()
	if len(res.Packets) != 5 {
		t.Fatalf("packets = %d, want 5", len(res.Packets))
	}
	if res.Packets[0].PayloadT != 96 {
		t.Fatalf("payload type = %d, want 96", res.Packets[0].PayloadT)
	}
}

func TestForeignSSRC(t *testing.T) {
	r := NewReorderer(8)
	r.Accept(testSSRC, 0, 0, false, 96, 0)
	r.Accept(0x1111, 1, 3000, false, 96, 1)
	res := r.Finish()
	if len(res.Anomalies) != 1 {
		t.Fatalf("anomalies = %+v", res.Anomalies)
	}
	if len(res.Packets) != 1 {
		t.Fatalf("packets = %d, want 1", len(res.Packets))
	}
}

func TestWindowBounds(t *testing.T) {
	if got := NewReorderer(0).Window(); got != DefaultWindow {
		t.Fatalf("default window = %d", got)
	}
	if got := NewReorderer(MaxWindow + 100).Window(); got != MaxWindow {
		t.Fatalf("clamped window = %d", got)
	}
}

func TestPeakPendingStaysBounded(t *testing.T) {
	r := NewReorderer(16)
	for i := 0; i < 500; i++ {
		r.Accept(testSSRC, uint16(i), uint32(i*3000), false, 96, int64(i))
	}
	res := r.Finish()
	if res.PeakPendingBytes > 16*pendingBytesPerPacket {
		t.Fatalf("peak pending = %d bytes, want <= %d", res.PeakPendingBytes, 16*pendingBytesPerPacket)
	}
}
