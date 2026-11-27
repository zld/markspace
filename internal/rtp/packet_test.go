package rtp

import (
	"testing"

	"mediakernel/internal/clock"
)

func TestExtractInOrder(t *testing.T) {
	tl := clock.NewTimeline(48000)
	ex, err := NewExtractor(tl, 42, 48000)
	if err != nil {
		t.Fatal(err)
	}
	pkts, err := GeneratePCMPackets(42, 48000, 5, 480)
	if err != nil {
		t.Fatal(err)
	}
	events, err := ex.ParseAll(pkts)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("events = %d", len(events))
	}
	// 480 samples at 48 kHz = 10 ms per packet.
	if events[1].MediaNanos != 10_000_000 {
		t.Fatalf("packet 1 ns = %d", events[1].MediaNanos)
	}
	if len(events[0].Payload) != 960 {
		t.Fatalf("payload bytes = %d", len(events[0].Payload))
	}
}

func TestExtractOutOfOrder(t *testing.T) {
	tl := clock.NewTimeline(48000)
	ex, _ := NewExtractor(tl, 7, 48000)
	pkts, _ := GeneratePCMPackets(7, 48000, 4, 480)
	// Arrival order: 0,2,1,3 — packet with seq 1 arrives after seq 2.
	shuffled := Reorder(Reorder(pkts, 1, 2), 2, 2)
	events, err := ex.ParseAll(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	// Each event maps by its own RTP timestamp regardless of arrival order.
	if events[0].MediaNanos != 0 || events[1].MediaNanos != 20_000_000 ||
		events[2].MediaNanos != 10_000_000 {
		t.Fatalf("reorder mapping wrong: %d %d %d",
			events[0].MediaNanos, events[1].MediaNanos, events[2].MediaNanos)
	}
}

func TestExtractTimestampWrap(t *testing.T) {
	tl := clock.NewTimeline(48000)
	ex, _ := NewExtractor(tl, 9, 48000)
	pkts, _ := GeneratePCMPackets(9, 48000, 2, 480)
	// Force origin near the 32-bit ceiling so the second packet wraps.
	pkts[0][4], pkts[0][5], pkts[0][6], pkts[0][7] = 0xFF, 0xFF, 0xFE, 0x00
	pkts[1][4], pkts[1][5], pkts[1][6], pkts[1][7] = 0x00, 0x00, 0x00, 0x40 // +576
	if _, err := ex.Parse(pkts[0]); err != nil {
		t.Fatal(err)
	}
	ev, err := ex.Parse(pkts[1])
	if err != nil {
		t.Fatalf("wrap packet rejected: %v", err)
	}
	if ev.MediaNanos != int64(576)*clock.NanosPerSecond/48000 {
		t.Fatalf("wrap ns = %d", ev.MediaNanos)
	}
}

func TestExtractBadDatagramKeepsSeq(t *testing.T) {
	tl := clock.NewTimeline(0)
	ex, _ := NewExtractor(tl, 1, 8000)
	bad := []byte{0x80, 0x60, 0x12, 0x34, 0x00, 0x00} // truncated fixed header
	_, err := ex.Parse(bad)
	ce, ok := clock.AsError(err)
	if !ok || !ce.HasSeq || ce.SeqNum != 0x1234 {
		t.Fatalf("error must carry seq 0x1234, got %v", err)
	}
}

func TestExtractSSRCConflict(t *testing.T) {
	tl := clock.NewTimeline(0)
	ex, _ := NewExtractor(tl, 1, 8000)
	pkts, _ := GeneratePCMPackets(2, 8000, 1, 10)
	_, err := ex.Parse(pkts[0])
	ce, ok := clock.AsError(err)
	if !ok || ce.Category != clock.CatLayoutMismatch || !ce.HasSeq {
		t.Fatalf("want ssrc conflict with seq, got %v", err)
	}
}
