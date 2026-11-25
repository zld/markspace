package segment

import (
	"testing"

	"mediad/internal/clock"
	"mediad/internal/stream"
)

func mkAU(index uint64, start, end int64, corrupt bool, data ...byte) *stream.AccessUnit {
	au := &stream.AccessUnit{Index: index, Start: start, End: end}
	if len(data) > 0 {
		au.NALs = []stream.NALUnit{{Data: data}}
	}
	if corrupt {
		au.Corrupt = true
		au.Reasons = []string{"truncated_stream"}
	}
	return au
}

func TestCutOneSegmentPerAU(t *testing.T) {
	tb := clock.NewTimeBase(40_000_000)
	aus := []*stream.AccessUnit{
		mkAU(0, 0, 10, false, 1, 2),
		mkAU(1, 10, 20, false, 3, 4),
		mkAU(2, 20, 30, true, 5),
	}
	segs := NewCutter(tb).Cut(aus, 1)
	if len(segs) != 3 {
		t.Fatalf("segments = %d", len(segs))
	}
	// Half-open timeline interval of frame 1: [40ms, 80ms).
	if segs[1].Interval.StartNS != 40_000_000 || segs[1].Interval.EndNS != 80_000_000 {
		t.Fatalf("interval = %+v", segs[1].Interval)
	}
	if string(segs[0].Payload) != "\x01\x02" {
		t.Fatalf("payload = %v", segs[0].Payload)
	}
	if segs[0].Corrupt || !segs[2].Corrupt || segs[2].Reasons[0] != "truncated_stream" {
		t.Fatalf("corruption propagation wrong: %+v", segs)
	}
	if segs[2].ByteStart != 20 || segs[2].ByteEnd != 30 {
		t.Fatalf("byte range = [%d,%d)", segs[2].ByteStart, segs[2].ByteEnd)
	}
}

func TestCutMergedSegmentsCarryCorruption(t *testing.T) {
	tb := clock.NewTimeBase(40_000_000)
	aus := []*stream.AccessUnit{
		mkAU(0, 0, 10, false, 1),
		mkAU(1, 10, 20, true, 2),
		mkAU(2, 20, 30, false, 3),
		mkAU(3, 30, 40, false, 4),
		mkAU(4, 40, 50, false, 5),
	}
	segs := NewCutter(tb).Cut(aus, 2)
	if len(segs) != 3 {
		t.Fatalf("merged segment count = %d", len(segs))
	}
	if segs[0].AUStart != 0 || segs[0].AUEnd != 2 || !segs[0].Corrupt {
		t.Fatalf("first merged segment: %+v", segs[0])
	}
	if segs[0].Interval.StartNS != 0 || segs[0].Interval.EndNS != 80_000_000 {
		t.Fatalf("merged interval = %+v", segs[0].Interval)
	}
	// Last partial group still uses the correct exclusive frame end.
	if segs[2].AUEnd != 5 || segs[2].Corrupt {
		t.Fatalf("tail segment: %+v", segs[2])
	}
}
