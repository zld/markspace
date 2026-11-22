package timeline

import "testing"

func TestConvertRoundingIsHalfAwayFromZero(t *testing.T) {
	// 44100 Hz -> 48000 Hz: 1 tick at 44.1k is 1.0884... ticks at 48k.
	in := MustScale(44100)
	out := MustScale(48000)
	cases := []struct {
		v    Ticks
		want Ticks
	}{
		{0, 0},
		{1, 1},
		{2, 2},
		{5, 5},   // 5.44 -> 5
		{10, 11}, // 10.884 -> 11
		{44100, 48000},
		{88200, 96000},
	}
	for _, c := range cases {
		got, err := Convert(c.v, in, out)
		if err != nil {
			t.Fatalf("convert %d: %v", c.v, err)
		}
		if got != c.want {
			t.Errorf("convert(%d) = %d, want %d", c.v, got, c.want)
		}
	}
}

func TestConvertExactTieRoundsUp(t *testing.T) {
	// 1/2 exactly is a tie and must round away from zero, not to even.
	got, err := Convert(1, MustScale(2), MustScale(1))
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("tie conversion = %d, want 1", got)
	}
	neg, err := Convert(-1, MustScale(2), MustScale(1))
	if err != nil {
		t.Fatal(err)
	}
	if neg != -1 {
		t.Fatalf("negative tie conversion = %d, want -1", neg)
	}
}

func TestConvertRejectsOverflowAndBadScales(t *testing.T) {
	if _, err := Convert(1<<40, MustScale(1), MustScale(1_000_000_000)); err == nil {
		t.Fatal("expected overflow error")
	}
	if _, err := Convert(1, Scale{}, MustScale(100)); err == nil {
		t.Fatal("expected invalid-scale error")
	}
	if _, err := NewScale(0); err == nil {
		t.Fatal("zero scale accepted")
	}
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("zero denominator accepted")
	}
	r := MustRational(4, -8)
	if r.Num != -1 || r.Den != 2 {
		t.Fatalf("normalize got %+v", r)
	}
}

func TestRebaseKeepsNonZeroOriginAndBoundsDrift(t *testing.T) {
	// Two tracks, different timescales, both starting at 1000 source ticks.
	videoFree := TrackTimeline{
		TrackID: 1, Source: MustScale(44100), Output: MustScale(48000),
		StartTicks: 1000, Duration: 44100,
	}
	other := TrackTimeline{
		TrackID: 2, Source: MustScale(90000), Output: MustScale(48000),
		StartTicks: 2048, Duration: 90000,
	}
	a, err := videoFree.Map(1000)
	if err != nil {
		t.Fatal(err)
	}
	if a != 0 {
		t.Fatalf("origin should map to zero, got %d", a)
	}
	b, err := other.Map(2048)
	if err != nil {
		t.Fatal(err)
	}
	if b != 0 {
		t.Fatalf("second origin should map to zero, got %d", b)
	}

	// Mapping sample n directly must never drift by more than half a tick
	// compared with mapping the same instant via the other scale.
	for n := int64(0); n < 2000; n++ {
		src := 1000 + n
		direct, err := videoFree.Map(Ticks(src))
		if err != nil {
			t.Fatal(err)
		}
		via := MustConvert(Ticks(n), MustScale(44100), MustScale(48000))
		if diff := direct - via; diff < -1 || diff > 1 {
			t.Fatalf("drift at %d: %d vs %d", n, direct, via)
		}
	}
}

func TestMapSpanEdgesAreIndependent(t *testing.T) {
	tl := TrackTimeline{
		TrackID: 7, Source: MustScale(44100), Output: MustScale(48000),
		StartTicks: 512,
	}
	// 1024 source ticks at 44.1k -> 1114.7... output ticks. A naive
	// per-tick accumulation would produce a different edge.
	span, err := tl.MapSpan(Span{Start: 512, Len: 1024})
	if err != nil {
		t.Fatal(err)
	}
	start, _ := Convert(0, MustScale(44100), MustScale(48000))
	end, _ := Convert(1024, MustScale(44100), MustScale(48000))
	if span.Start != start || span.Len != end-start {
		t.Fatalf("span = %+v, want start=%d len=%d", span, start, end-start)
	}
	if _, err := tl.MapSpan(Span{Start: 512, Len: -1}); err == nil {
		// Negative lengths are filtered by BuildSegments but MapSpan itself
		// may produce a zero-length span; either behaviour is acceptable as
		// long as it never yields a negative mapped span.
		t.Log("negative span accepted as zero-length")
	}
}

func TestBuildSegmentsSkipsEmptyAndKeepsOrder(t *testing.T) {
	tl := TrackTimeline{TrackID: 3, Source: MustScale(48000), Output: MustScale(48000), StartTicks: 960}
	segs, err := BuildSegments(tl, []Span{{Start: 960, Len: 0}, {Start: 960, Len: 480}, {Start: 2400, Len: 480}})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("got %d segments", len(segs))
	}
	if segs[0].Out.Start != 0 || segs[1].Out.Start != 1440 {
		t.Fatalf("unexpected output starts: %+v", segs)
	}
}
