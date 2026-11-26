package rtpclock

import (
	"testing"

	"github.com/pion/rtp"
)

func pkt(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: ts},
		Payload: []byte{1},
	}
}

func TestBasicAndTicks(t *testing.T) {
	s, err := NewStream("src", 90000)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := s.Observe(pkt(0, 90000))
	if err != nil || ev.Kind != EventOK || int64(ev.Ticks) != 0 {
		t.Fatalf("first packet: %+v err=%v", ev, err)
	}
	ev, _ = s.Observe(pkt(1, 180000))
	if ev.Kind != EventOK || int64(ev.Ticks) != 90000 || ev.Unwrapped != 180000 {
		t.Fatalf("second packet ticks/unwrapped wrong: %+v", ev)
	}
}

func TestDuplicate(t *testing.T) {
	s, _ := NewStream("src", 90000)
	s.Observe(pkt(5, 100))
	ev, _ := s.Observe(pkt(5, 100))
	if ev.Kind != EventDuplicate {
		t.Fatalf("expected duplicate, got %s", ev.Kind)
	}
}

func TestReordered(t *testing.T) {
	s, _ := NewStream("src", 90000)
	s.Observe(pkt(10, 1000))
	s.Observe(pkt(11, 2000))
	ev, _ := s.Observe(pkt(9, 900))
	if ev.Kind != EventReordered || ev.BackJump != 2 {
		t.Fatalf("expected reordered with back jump 2, got %+v", ev)
	}
	// Reordered timestamp maps below the current head but stays consistent.
	if int64(ev.Ticks) < 0 {
		t.Fatalf("reordered tick must stay non-negative: %d", ev.Ticks)
	}
}

func TestLost(t *testing.T) {
	s, _ := NewStream("src", 90000)
	s.Observe(pkt(0, 0))
	ev, _ := s.Observe(pkt(3, 300))
	if ev.Kind != EventLost || ev.LostCount != 2 {
		t.Fatalf("expected 2 lost, got %+v", ev)
	}
}

func TestTimestampWrap(t *testing.T) {
	s, _ := NewStream("src", 90000)
	// Start near the top of the 32-bit space.
	first, err := s.Observe(pkt(0, 0xFFFFFFF0))
	if err != nil || first.Kind != EventOK {
		t.Fatalf("wrap setup: %+v %v", first, err)
	}
	ev, err := s.Observe(pkt(1, 0x00000010))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != EventWrap {
		t.Fatalf("expected wrap event, got %s", ev.Kind)
	}
	want := uint64(0x100000010)
	if ev.Unwrapped != want {
		t.Fatalf("unwrapped = %#x, want %#x", ev.Unwrapped, want)
	}
	if ev.Ticks <= first.Ticks {
		t.Fatalf("post-wrap ticks must advance: first=%d wrap=%d", first.Ticks, ev.Ticks)
	}
}

func TestSequenceWrapIsNotLost(t *testing.T) {
	s, _ := NewStream("src", 90000)
	s.Observe(pkt(65535, 100))
	ev, _ := s.Observe(pkt(0, 200))
	if ev.Kind != EventOK || ev.LostCount != 0 {
		t.Fatalf("16-bit sequence rollover must be clean, got %+v", ev)
	}
}

func TestZeroClockRateRejected(t *testing.T) {
	if _, err := NewStream("src", 0); err == nil {
		t.Fatal("zero clock rate must be rejected")
	}
}
