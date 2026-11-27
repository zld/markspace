package rng

import (
	"testing"

	"tracecheck/internal/clock"
	"tracecheck/internal/index"
	"tracecheck/internal/mkv"
	"tracecheck/internal/serr"
)

func fixtureIndex() (*index.Index, []byte) {
	// Five 10-byte frames at 0,40,80,120,160 ms across two tracks.
	data := make([]byte, 500)
	var blocks []mkv.Block
	times := []int64{0, 40, 80, 120, 160}
	tracks := []uint64{1, 2, 1, 1, 2}
	for i, ticks := range times {
		start := int64(i * 10)
		copy(data[start:start+10], []byte{byte(i), 1, 2, 3, 4, 5, 6, 7, 8, 9})
		blocks = append(blocks, mkv.Block{
			TrackNumber:  tracks[i],
			Index:        i,
			Keyframe:     i%2 == 0,
			PayloadStart: start,
			PayloadEnd:   start + 10,
			Cluster: mkv.ClusterRef{
				Index: i / 3, Offset: start - 1, End: start + 100, Timestamp: ticks,
			},
		})
	}
	tb := clock.Timebase{TickNanos: clock.NanosPerMilli, ClientUnitNanos: clock.NanosPerMilli}
	ix, _ := index.Build(blocks, tb, clock.NanosPerMilli, "1ms")
	return ix, data
}

func TestReadHalfOpenRange(t *testing.T) {
	ix, data := fixtureIndex()
	// [40ms, 120ms) must include the 40ms and 80ms blocks, exclude 120ms.
	res, err := Read(ix, data, 40*clock.NanosPerMilli, 120*clock.NanosPerMilli)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fragments) != 2 {
		t.Fatalf("fragments=%d, want 2", len(res.Fragments))
	}
	if res.Fragments[0].TimeNanos != 40*clock.NanosPerMilli ||
		res.Fragments[1].TimeNanos != 80*clock.NanosPerMilli {
		t.Errorf("fragment times=%v", []int64{res.Fragments[0].TimeNanos, res.Fragments[1].TimeNanos})
	}
	// Exact byte slices, never rewritten.
	if res.Fragments[0].Bytes[0] != 1 || res.Fragments[1].Bytes[0] != 2 {
		t.Errorf("fragment bytes wrong")
	}
	if res.SegmentHash() == "" || len(res.SegmentHash()) != 64 {
		t.Errorf("segment hash=%q", res.SegmentHash())
	}
}

func TestReadStableSegmentHash(t *testing.T) {
	ix, data := fixtureIndex()
	a, _ := Read(ix, data, 0, 160*clock.NanosPerMilli)
	b, _ := Read(ix, data, 0, 160*clock.NanosPerMilli)
	if a.SegmentHash() != b.SegmentHash() {
		t.Fatal("segment hash not repeatable")
	}
}

func TestReadReportsActualBoundary(t *testing.T) {
	ix, data := fixtureIndex()
	res, err := Read(ix, data, 10*clock.NanosPerMilli, 100*clock.NanosPerMilli)
	if err != nil {
		t.Fatal(err)
	}
	// First block is at 40ms, so the actual start is later than requested.
	if res.ActualStart != 40*clock.NanosPerMilli {
		t.Errorf("actual start=%d", res.ActualStart)
	}
	if res.Reason == "" {
		t.Error("expected a reason for the shifted boundary")
	}
}

func TestReadOutOfBounds(t *testing.T) {
	ix, data := fixtureIndex()
	if _, err := Read(ix, data, 200*clock.NanosPerMilli, 300*clock.NanosPerMilli); serr.CodeOf(err) != serr.CodeRangeOutOfBounds {
		t.Fatalf("past-last range: %v", err)
	}
	if _, err := Read(ix, data, 100, 100); serr.CodeOf(err) != serr.CodeInvalidArgs {
		t.Fatalf("empty range: %v", err)
	}
}

func TestReadBeforeFirstIsEmpty(t *testing.T) {
	ix, data := fixtureIndex()
	// Range entirely before the first block: empty but not an error.
	res, err := Read(ix, data, -100, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fragments) != 0 || !res.Truncated {
		t.Fatalf("before-first result: %+v", res)
	}
}

func TestReadDetectsCorruptOffsets(t *testing.T) {
	ix, data := fixtureIndex()
	// Shrink the backing data so an indexed offset now exceeds it.
	if _, err := Read(ix, data[:5], 0, 160*clock.NanosPerMilli); serr.CodeOf(err) != serr.CodeRangeOutOfBounds {
		t.Fatalf("corrupt offsets not caught: %v", err)
	}
}
