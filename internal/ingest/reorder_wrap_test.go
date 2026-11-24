package ingest_test

import (
	"math"
	"testing"

	"medpipe/internal/clock"
	"medpipe/internal/ingest"
)

func TestReorderBufferDeliversOrderAcrossTimestampWrap(t *testing.T) {
	tl := clock.NewTimeline(48000)
	rb := ingest.NewReorderBuffer(ingest.ReorderConfig{GapThreshold: 8})
	const ssrc = 77
	const step = uint32(960)
	start := uint32(math.MaxUint32) - step // packet 1 reaches uint32 max; packet 2 wraps

	makeItem := func(i int) ingest.Item {
		seq := uint16(4000 + i)
		ts := start + uint32(i)*step
		return ingest.Item{
			Kind: "rtp", SSRC: ssrc, Sequence: seq, Timestamp: ts,
			ExtSeq: tl.ExtendSequence(ssrc, seq), ExtTS: tl.ExtendTimestamp(ssrc, ts),
		}
	}

	order := []int{0, 2, 1, 1 /* duplicate */, 4, 3, 5}
	var delivered []uint16
	for _, i := range order {
		for _, out := range rb.Submit(makeItem(i)) {
			delivered = append(delivered, out.Sequence)
		}
	}
	for _, out := range rb.Flush() {
		delivered = append(delivered, out.Sequence)
	}

	want := []uint16{4000, 4001, 4002, 4003, 4004, 4005}
	if len(delivered) != len(want) {
		t.Fatalf("delivered=%v want %v", delivered, want)
	}
	for i := range want {
		if delivered[i] != want[i] {
			t.Fatalf("delivered order=%v want %v", delivered, want)
		}
	}
	if rb.Stats.Duplicates != 1 {
		t.Fatalf("duplicates=%d want 1", rb.Stats.Duplicates)
	}
	if rb.Stats.OutOfOrder < 2 {
		t.Fatalf("out_of_order=%d want at least 2", rb.Stats.OutOfOrder)
	}

	// Extended timestamps of delivered sequences remain monotonic despite wrap.
	var last uint64
	for i, seq := range want {
		ts := start + uint32(seq-4000)*step
		ext := tl.ExtendTimestamp(ssrc, ts)
		if i > 0 && ext <= last {
			t.Fatalf("extended timestamp not monotonic at seq %d: %d <= %d", seq, ext, last)
		}
		last = ext
	}
}
