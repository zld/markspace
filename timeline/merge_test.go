package timeline

import (
	"errors"
	"testing"
)

func ev(track uint32, kind Kind, tick, seq int64) Event {
	return Event{TrackID: track, Kind: kind, Tick: tick, Seq: seq}
}

func TestNormalizeBasicAndNonZeroOrigin(t *testing.T) {
	tr := Track{ID: 1, Kind: KindVideo, Clock: Clock{Timescale: 90000, OriginNS: 5_000_000},
		Events: []Event{ev(1, KindVideo, 0, 0), ev(1, KindVideo, 90000, 1)}}
	if err := NormalizeTrack(&tr, 0, 0); err != nil {
		t.Fatal(err)
	}
	if tr.Events[0].TimeNS != 5_000_000 || tr.Events[1].TimeNS != 1_005_000_000 {
		t.Fatalf("unexpected times: %d %d", tr.Events[0].TimeNS, tr.Events[1].TimeNS)
	}
}

func TestNormalizeWraparound32bit(t *testing.T) {
	clk := Clock{Timescale: 90000, WrapModulus: 1 << 32}
	tr := Track{ID: 2, Clock: clk, Events: []Event{
		ev(2, KindSEI, (1<<32)-90000, 0),
		ev(2, KindSEI, 90000, 1), // wrapped: absolute = 2^32 + 90000
	}}
	if err := NormalizeTrack(&tr, 0, 0); err != nil {
		t.Fatal(err)
	}
	delta := tr.Events[1].TimeNS - tr.Events[0].TimeNS
	if delta != 2_000_000_000 {
		t.Fatalf("wrap delta=%d want 2s", delta)
	}
	// The first raw tick anchors at absolute zero; wrapped values stay small.
	if tr.Events[0].TimeNS != 0 {
		t.Fatalf("first wrapped tick must anchor at 0, got %d", tr.Events[0].TimeNS)
	}
	if tr.Events[1].TimeNS != 2_000_000_000 {
		t.Fatalf("unwrapped second tick time=%d want 2s", tr.Events[1].TimeNS)
	}
}

func TestNormalizeDriftTolerance(t *testing.T) {
	// 5ms backward step tolerated; 50ms rejected when no wrap modulus is set.
	clk := Clock{Timescale: 1000, MaxDriftNS: 10_000_000}
	ok := Track{ID: 3, Clock: clk, Events: []Event{ev(3, KindID3, 100, 0), ev(3, KindID3, 95, 1)}}
	if err := NormalizeTrack(&ok, 0, 0); err != nil {
		t.Fatalf("small drift should be tolerated: %v", err)
	}
	bad := Track{ID: 4, Clock: clk, Events: []Event{ev(4, KindID3, 100, 0), ev(4, KindID3, 50, 1)}}
	err := NormalizeTrack(&bad, 0, 0)
	if err == nil || !errors.Is(err, ErrBackwards) {
		t.Fatalf("want backwards error, got %v", err)
	}
}

func TestNegativeAndOutOfBounds(t *testing.T) {
	neg := Track{ID: 5, Clock: Clock{Timescale: 1000, OriginNS: -1}, Events: []Event{ev(5, KindEmsg, 0, 0)}}
	if err := NormalizeTrack(&neg, 0, 0); err == nil || !errors.Is(err, ErrNegative) {
		t.Fatalf("want negative error, got %v", err)
	}
	oob := Track{ID: 6, Clock: Clock{Timescale: 1000}, Events: []Event{ev(6, KindEmsg, 2000, 0)}}
	if err := NormalizeTrack(&oob, 0, 1_000_000_000); err == nil || !errors.Is(err, ErrOutOfBounds) {
		t.Fatalf("want oob error, got %v", err)
	}
}

func TestMergeStableOrderAndDedupe(t *testing.T) {
	t1 := Track{ID: 1, Kind: KindVideo, Clock: Clock{Timescale: 1000},
		Events: []Event{ev(1, KindVideo, 100, 0), ev(1, KindVideo, 100, 0), ev(1, KindVideo, 200, 1)}}
	t2 := Track{ID: 2, Kind: KindSubtitle, Clock: Clock{Timescale: 1000},
		Events: []Event{ev(2, KindSubtitle, 100, 0)}}
	tl, err := Merge([]Track{t1, t2}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tl.Stats.Duplicates != 1 || tl.Stats.EventCount != 3 {
		t.Fatalf("stats=%+v", tl.Stats)
	}
	want := []struct {
		track uint32
		seq   int64
	}{{1, 0}, {2, 0}, {1, 1}}
	for i, w := range want {
		if tl.Events[i].TrackID != w.track || tl.Events[i].Seq != w.seq {
			t.Fatalf("pos %d got track=%d seq=%d", i, tl.Events[i].TrackID, tl.Events[i].Seq)
		}
	}
}

func TestMergeRejectsDuplicateTrackAndBadClock(t *testing.T) {
	a := Track{ID: 7, Clock: Clock{Timescale: 0}, Events: []Event{ev(7, KindChapter, 0, 0)}}
	if _, err := Merge([]Track{a}, 0, 0); err == nil {
		t.Fatal("bad clock must be rejected")
	}
	b := Track{ID: 8, Clock: Clock{Timescale: 1}}
	c := Track{ID: 8, Clock: Clock{Timescale: 1}}
	if _, err := Merge([]Track{b, c}, 0, 0); err == nil {
		t.Fatal("duplicate track id must be rejected")
	}
}

func TestMergeDeterministicAcrossRuns(t *testing.T) {
	mk := func() []Track {
		return []Track{
			{ID: 3, Clock: Clock{Timescale: 1000}, Events: []Event{ev(3, KindID3, 500, 0), ev(3, KindID3, 100, 1)}},
			{ID: 1, Clock: Clock{Timescale: 1000}, Events: []Event{ev(1, KindVideo, 500, 0), ev(1, KindVideo, 100, 0)}},
		}
	}
	tl1, err := Merge(mk(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tl2, err := Merge(mk(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tl1.Events {
		a, b := tl1.Events[i], tl2.Events[i]
		if a.TrackID != b.TrackID || a.Seq != b.Seq || a.TimeNS != b.TimeNS || a.Kind != b.Kind {
			t.Fatalf("non-deterministic merge at %d", i)
		}
	}
}
