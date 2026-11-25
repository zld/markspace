package index

import (
	"testing"

	"mediad/internal/clock"
	"mediad/internal/stream"
)

func au(index uint64, start int64, key, corrupt bool) *stream.AccessUnit {
	return &stream.AccessUnit{Index: index, Start: start, End: start + 10,
		Keyframe: key, Corrupt: corrupt}
}

func TestMatchTimelineExactOnly(t *testing.T) {
	aus := []*stream.AccessUnit{
		au(0, 100, true, false),
		au(1, 120, false, false),
		au(2, 200, true, true), // keyframe inside a corrupt AU
		au(3, 300, true, false),
	}
	idx := &Index{
		ValidCount: 4,
		Entries: []Entry{
			{Raw: RawEntry{Frame: 0, Off: 100, Type: "IDR"}, Valid: true},
			{Raw: RawEntry{Frame: 1, Off: 200, Type: "IDR"}, Valid: true},
			{Raw: RawEntry{Frame: 2, Off: 300, Type: "IDR"}, Valid: true},
			{Raw: RawEntry{Frame: 3, Off: 250, Type: "IDR"}, Valid: true}, // miss
		},
	}
	tb := clock.NewTimeBase(40_000_000)
	rep := MatchTimeline(aus, idx, tb)
	if rep.Hits != 3 || rep.Misses != 1 {
		t.Fatalf("hits/misses = %d/%d", rep.Hits, rep.Misses)
	}
	if rep.HitRate != 0.75 {
		t.Fatalf("hit rate = %f, want 0.75", rep.HitRate)
	}
	if len(rep.DamagedOffsets) != 1 || rep.DamagedOffsets[0] != 200 {
		t.Fatalf("damaged offsets = %v", rep.DamagedOffsets)
	}
	// All stream keyframes are referenced, so no extras.
	if len(rep.ExtraKeyframes) != 0 {
		t.Fatalf("unexpected extras %v", rep.ExtraKeyframes)
	}
	// A hit carries the nanosecond timeline point.
	if rep.Matches[0].Point.Offset != 0 || rep.Matches[1].Point.Offset != 80_000_000 {
		t.Fatalf("timeline points wrong: %+v", rep.Matches)
	}
	// The miss keeps its offset and records a reason; it is not rebound.
	miss := rep.Matches[3]
	if miss.Hit || miss.AU != -1 || miss.Reason == "" {
		t.Fatalf("miss rebound or missing reason: %+v", miss)
	}
}

func TestMatchTimelineExtrasAndInvalid(t *testing.T) {
	aus := []*stream.AccessUnit{au(0, 10, true, false), au(1, 50, true, false)}
	idx := &Index{
		ValidCount:   1,
		InvalidCount: 1,
		Entries: []Entry{
			{Raw: RawEntry{Frame: 0, Off: 10, Type: "IDR"}, Valid: true},
			{Raw: RawEntry{Frame: 1, Off: 9, Type: "IDR"}, Valid: false,
				Reason: "offset 9 not strictly greater"},
		},
	}
	rep := MatchTimeline(aus, idx, clock.NewTimeBase(40_000_000))
	if rep.HitRate != 1.0 || rep.Hits != 1 {
		t.Fatalf("hit rate must count valid entries only: %+v", rep)
	}
	if len(rep.ExtraKeyframes) != 1 || rep.ExtraKeyframes[0] != 50 {
		t.Fatalf("extra keyframes = %v", rep.ExtraKeyframes)
	}
	if rep.Matches[1].Valid || rep.Matches[1].Hit {
		t.Fatal("invalid entry must never become a hit")
	}
}
