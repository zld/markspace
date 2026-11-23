package model

import "testing"

func TestNewRatNormalizes(t *testing.T) {
	r, err := NewRat(6, 4)
	if err != nil {
		t.Fatalf("NewRat: %v", err)
	}
	if r.Num != 3 || r.Den != 2 {
		t.Fatalf("got %d/%d, want 3/2", r.Num, r.Den)
	}
	if _, err := NewRat(1, 0); err == nil {
		t.Fatal("zero denominator must error")
	}
}

func TestRatCompareAcrossTimescales(t *testing.T) {
	// 30000/48000 == 20000/32000 must compare equal without floats.
	a := MustRat(30000, 48000)
	b := MustRat(20000, 32000)
	if a.Cmp(b) != 0 {
		t.Fatalf("expected equal timeline points, got %s vs %s", a, b)
	}
	if a.Add(MustRat(1, 2)).Cmp(MustRat(9, 8)) != 0 {
		t.Fatal("rational add mismatch")
	}
}

func TestRatOverflowDetected(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on overflow comparison")
		}
	}()
	big := Rat{Num: 1<<62, Den: 1}
	big.Cmp(Rat{Num: 1, Den: 1 << 62})
}

func TestJoinRangesMerges(t *testing.T) {
	r1 := Range{Start: MustRat(0, 1), End: MustRat(2, 1)}
	r2 := Range{Start: MustRat(1, 1), End: MustRat(3, 1)}
	r3 := Range{Start: MustRat(5, 1), End: MustRat(6, 1)}
	got := JoinRanges([]Range{r3, r1, r2})
	if len(got) != 2 {
		t.Fatalf("got %d ranges, want 2: %v", len(got), got)
	}
	if got[0].End.Cmp(MustRat(3, 1)) != 0 || got[1].Start.Cmp(MustRat(5, 1)) != 0 {
		t.Fatalf("unexpected merge result: %v", got)
	}
}

func TestErrorSetDeterministic(t *testing.T) {
	var es ErrorSet
	es.Add(MediaError{Kind: KindTimestampGap, TrackID: 2, SampleIndex: 4})
	es.Add(MediaError{Kind: KindTruncatedBox, TrackID: 0, SampleIndex: -1})
	es.Add(MediaError{Kind: KindTimestampGap, TrackID: 1, SampleIndex: 0})
	got := es.All()
	want := []ErrorKind{KindTruncatedBox, KindTimestampGap, KindTimestampGap}
	for i, w := range want {
		if got[i].Kind != w {
			t.Fatalf("position %d: got %s want %s", i, got[i].Kind, w)
		}
	}
}

func TestTopoOrderAndCycle(t *testing.T) {
	v := &Track{ID: 1, Kind: KindVideo}
	a := &Track{ID: 2, Kind: KindAudio, DependsOn: []uint32{1}}
	g, err := NewGraph([]*Track{a, v})
	if err != nil {
		t.Fatal(err)
	}
	order, err := g.TopoOrder()
	if err != nil || len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("topo order = %v, err=%v", order, err)
	}

	loop := &Track{ID: 3, Kind: KindAudio, DependsOn: []uint32{4}}
	loop2 := &Track{ID: 4, Kind: KindAudio, DependsOn: []uint32{3}}
	g2, err := NewGraph([]*Track{loop, loop2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g2.TopoOrder(); err == nil {
		t.Fatal("expected cycle error")
	}
}
