package segment

import (
	"testing"

	"mediatool/internal/index"
)

func track(id uint32, startMs, endMs int64, kf ...int64) *index.Track {
	tr := &index.Track{TrackID: id, Time: index.TimeRange{Valid: true, StartMs: startMs, EndMs: endMs}}
	for i, ms := range kf {
		tr.Keyframes = append(tr.Keyframes, index.Keyframe{MediaMs: ms, Offset: int64(i+1) * 100, Seq: uint16(i)})
	}
	return tr
}

func TestAlignSharesBoundaries(t *testing.T) {
	a := track(1, 0, 5000, 0, 1000, 2000, 3000, 4000)
	b := track(2, 0, 5000, 0, 1000, 2000, 3000, 4000)
	res, err := Align([]Coverage{{ID: "a", Track: a}, {ID: "b", Track: b}}, Config{DurationMs: 1000})
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	if len(res.Segments) == 0 {
		t.Fatal("no segments")
	}
	for _, seg := range res.Segments {
		if len(seg.Representations) != 2 {
			t.Fatalf("segment %d has %d representations, want 2", seg.Index, len(seg.Representations))
		}
		// Both representations must start at the same boundary.
		if seg.Representations[0].KeyframeSeq != seg.Representations[1].KeyframeSeq {
			t.Fatalf("segment %d boundaries diverge", seg.Index)
		}
	}
	if res.MaxSkewMs != 0 {
		t.Fatalf("max skew = %d, want 0 for aligned keyframes", res.MaxSkewMs)
	}
}

func TestAlignSearchesForwardWithinBudget(t *testing.T) {
	// Keyframes at 1500 for a 1000 boundary: 500ms of forward search.
	a := track(1, 0, 3000, 1500, 2500)
	res, err := Align([]Coverage{{ID: "a", Track: a}}, Config{DurationMs: 1000, KeyframeSearchMs: 800})
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	if len(res.Segments) == 0 {
		t.Fatal("expected a forward-searched segment")
	}
	if res.MaxSkewMs != 500 {
		t.Fatalf("skew = %d, want 500", res.MaxSkewMs)
	}
}

func TestAlignReportsUnalignableWhenBudgetExceeded(t *testing.T) {
	// Sparse keyframes: only every 2000ms on a 1000ms grid with a 500ms budget.
	a := track(1, 0, 4000, 0, 2000, 4000)
	res, err := Align([]Coverage{{ID: "a", Track: a}}, Config{DurationMs: 1000, KeyframeSearchMs: 500})
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	if len(res.Unaligned) == 0 {
		t.Fatal("expected unalignable boundaries")
	}
	if len(res.Faults) == 0 {
		t.Fatal("expected alignment faults for unalignable boundaries")
	}
}

func TestAlignRejectsUntrustedKeyframes(t *testing.T) {
	// A keyframe exists inside the budget but sits in an untrusted interval.
	a := track(1, 0, 3000, 1500)
	trusted := func(ms int64) bool { return ms < 1000 }
	res, err := Align([]Coverage{{ID: "a", Track: a, Trusted: trusted}}, Config{DurationMs: 1000, KeyframeSearchMs: 800})
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	if len(res.Segments) != 0 {
		t.Fatalf("segments = %d, want none: the only keyframe is untrusted", len(res.Segments))
	}
	if len(res.Unaligned) == 0 {
		t.Fatal("expected the untrusted boundary to be reported unalignable")
	}
}

func TestAlignRequiresOverlappingRanges(t *testing.T) {
	a := track(1, 0, 1000, 0)
	b := track(2, 5000, 6000, 5000)
	_, err := Align([]Coverage{{ID: "a", Track: a}, {ID: "b", Track: b}}, Config{DurationMs: 1000})
	if err == nil {
		t.Fatal("expected error for non-overlapping time ranges")
	}
}

func TestAlignRequiresTrustedTimeRange(t *testing.T) {
	a := &index.Track{TrackID: 1}
	_, err := Align([]Coverage{{ID: "a", Track: a}}, Config{DurationMs: 1000})
	if err == nil {
		t.Fatal("expected error for a track with no trusted time")
	}
}
