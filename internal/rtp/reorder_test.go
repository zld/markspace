package rtp

import (
	"testing"

	"github.com/local/medialab/internal/fixture"
	"github.com/pion/rtp"
)

func TestReorderDedupAndSort(t *testing.T) {
	buf := NewReorderBuffer(DefaultReorderConfig())
	pkts, _ := fixture.GenerateRTPSession(7, 12, 4, 100, 90000)
	var media []*rtp.Packet
	for _, p := range pkts {
		if p.PayloadType == 96 {
			media = append(media, p)
		}
	}
	// Push seqs 100 and 102 with a gap at 101. The buffer never drains on
	// Push alone; both are held in sorted order until Drain is called.
	p0, p1, p2 := media[0], media[1], media[2]
	buf.Push(p0.SequenceNumber, p0.Timestamp, p0.Payload, false, 0)
	buf.Push(p2.SequenceNumber, p2.Timestamp, p2.Payload, false, 1)
	if buf.Buffered() != 2 {
		t.Fatalf("buffered = %d, want 2", buf.Buffered())
	}
	// Duplicate of the held packet is ignored without growth.
	buf.Push(p2.SequenceNumber, p2.Timestamp, p2.Payload, false, 2)
	if buf.Stats().Duplicates != 1 {
		t.Fatalf("in-buffer duplicates = %d, want 1", buf.Stats().Duplicates)
	}
	// Only the contiguous prefix (100) drains while the gap persists.
	partial := buf.Drain()
	if len(partial) != 1 || partial[0].seq != p0.SequenceNumber {
		t.Fatalf("partial drain = %v, want only 100", partial)
	}
	// Filling the gap flushes 101 then 102 in order.
	buf.Push(p1.SequenceNumber, p1.Timestamp, p1.Payload, false, 3)
	got := buf.Drain()
	if len(got) != 2 {
		t.Fatalf("drained %d, want 2 (101,102)", len(got))
	}
	if got[0].seq != p1.SequenceNumber || got[1].seq != p2.SequenceNumber {
		t.Fatalf("order = %d,%d want %d,%d",
			got[0].seq, got[1].seq, p1.SequenceNumber, p2.SequenceNumber)
	}
	// Post-delivery duplicate detected through the bounded delivery ring.
	buf.Push(p1.SequenceNumber, p1.Timestamp, p1.Payload, false, 4)
	if buf.Stats().Duplicates != 2 {
		t.Fatalf("post-delivery duplicates = %d, want 2", buf.Stats().Duplicates)
	}
}

func TestLatePacketOutsideWindow(t *testing.T) {
	cfg := DefaultReorderConfig()
	cfg.WindowSize = 8
	buf := NewReorderBuffer(cfg)
	// Deliver enough packets that seq 0 leaves both the window and the
	// bounded duplicate-detection ring.
	for seq := uint16(0); seq < 80; seq++ {
		buf.Push(seq, 90000+uint32(seq)*3000, []byte{byte(seq)}, false, int64(seq))
		buf.Drain()
	}
	before := buf.Stats()
	buf.Push(0, 90000, []byte{99}, false, 500)
	if buf.Stats().Late != before.Late+1 {
		t.Fatalf("window-external packet not marked late: %+v", buf.Stats())
	}
	if buf.Buffered() != 0 {
		t.Fatalf("late packet buffered anyway: %d held", buf.Buffered())
	}
}

func TestTimestampWrapNeverReverses(t *testing.T) {
	buf := NewReorderBuffer(DefaultReorderConfig())
	buf.Push(0, 0xFFFFFFF0, []byte{1}, false, 0)
	axisBefore := buf.TimestampAxis()
	buf.Push(1, 0x00000010, []byte{2}, false, 1)
	axisAfter := buf.TimestampAxis()
	if axisAfter <= axisBefore {
		t.Fatalf("timestamp axis reversed across wrap: %d -> %d", axisBefore, axisAfter)
	}
	if axisAfter-axisBefore != 32 {
		t.Fatalf("wrap distance = %d, want 32", axisAfter-axisBefore)
	}
}

func TestWaitForBandwidthBounded(t *testing.T) {
	cfg := DefaultReorderConfig()
	cases := map[int]int64{
		10000: cfg.MinWaitTicks,
		1:     cfg.MaxWaitTicks,
		0:     cfg.MaxWaitTicks,
	}
	for kbps, want := range cases {
		if got := cfg.WaitForBandwidth(kbps); got != want {
			t.Fatalf("bw=%d wait=%d, want %d", kbps, got, want)
		}
	}
	if lo, hi := cfg.WaitForBandwidth(300), cfg.WaitForBandwidth(900); lo < hi {
		t.Fatalf("wait must not shrink when bandwidth drops: %d < %d", lo, hi)
	}
}

func TestBufferHardCap(t *testing.T) {
	cfg := DefaultReorderConfig()
	cfg.MaxBuffered = 4
	buf := NewReorderBuffer(cfg)
	// Push non-contiguous seqs so nothing drains.
	for _, seq := range []uint16{0, 2, 4, 6, 8, 10} {
		buf.Push(seq, 90000+uint32(seq)*3000, []byte{byte(seq)}, false, int64(seq))
	}
	if buf.Buffered() > 4 {
		t.Fatalf("buffer grew past cap: %d", buf.Buffered())
	}
	if buf.Stats().Late == 0 {
		t.Fatal("overflow push should be counted late")
	}
}
