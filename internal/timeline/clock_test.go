package timeline

import (
	"testing"
)

func TestNewRationalNormalizes(t *testing.T) {
	r, err := NewRational(6, -8)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != -3 || r.Den != 4 {
		t.Fatalf("got %v", r)
	}
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("expected zero denominator rejection")
	}
}

func TestScaleIsExactAndFloors(t *testing.T) {
	cases := []struct {
		ticks    int64
		from, to uint32
		want     int64
	}{
		{90000, 90000, 1000, 1000},
		{1, 90000, 1000, 0},       // floor
		{89999, 90000, 1000, 999}, // floor at the boundary
		{9000, 90000, 48000, 4800},
		{1 << 40, 1 << 30, 1000, 1 << 10 * 1000}, // large but exact
	}
	for _, c := range cases {
		got, err := Scale(c.ticks, c.from, c.to)
		if err != nil {
			t.Fatalf("Scale(%d,%d,%d): %v", c.ticks, c.from, c.to, err)
		}
		if got != c.want {
			t.Fatalf("Scale(%d,%d,%d)=%d want %d", c.ticks, c.from, c.to, got, c.want)
		}
	}
	if _, err := Scale(1, 0, 1000); err == nil {
		t.Fatal("zero timescale must fail")
	}
	if _, err := Scale(-1, 1000, 1000); err == nil {
		t.Fatal("negative ticks must fail")
	}
}

func TestScaleRound(t *testing.T) {
	// 1.5 -> 2, 1.4 -> 1 for a 10:1 conversion.
	if got, err := ScaleRound(15, 10, 1); err != nil || got != 2 {
		t.Fatalf("ScaleRound(15,10,1)=%d,%v", got, err)
	}
	if got, err := ScaleRound(14, 10, 1); err != nil || got != 1 {
		t.Fatalf("ScaleRound(14,10,1)=%d,%v", got, err)
	}
	if got, err := ScaleRound(0, 10, 1); err != nil || got != 0 {
		t.Fatalf("ScaleRound(0)=%d,%v", got, err)
	}
}

func TestClockExpandHandlesWrap(t *testing.T) {
	c, err := NewClock(90000, 32)
	if err != nil {
		t.Fatal(err)
	}
	// Prime near the 32-bit wrap.
	start := int64(0xFFFFFF00)
	c.ref = start
	c.primed = true
	// Raw 0x00000020 is 0x120 past start across the wrap: 0x100 to wrap plus
	// 0x20 after it.
	got := c.Expand(0x20)
	want := start + 0x120
	if got != want {
		t.Fatalf("expand across wrap got %d want %d", got, want)
	}
	// A reordered older packet just before the wrap must map below start.
	got = c.ExpandFrom(0xFFFFFE00, start)
	if got != int64(0xFFFFFE00) {
		t.Fatalf("expand before wrap got %d", got)
	}
}

func TestClockNonZeroOriginAndReset(t *testing.T) {
	c, err := NewClock(48000, 32)
	if err != nil {
		t.Fatal(err)
	}
	c.Start = 48000 * 5 // five seconds in
	if got := c.Expand(1000); got != 48000*5+1000 {
		t.Fatalf("first expand %d", got)
	}
	if got := c.OriginNS(); got != 5_000_000_000 {
		t.Fatalf("origin ns %d", got)
	}
	c.Reset()
	if c.primed {
		t.Fatal("reset must clear priming")
	}
	if got := c.Expand(2000); got != 48000*5+2000 {
		t.Fatalf("post-reset expand %d", got)
	}
}

func TestClockToAndFromNS(t *testing.T) {
	c, _ := NewClock(90000, 32)
	if got := c.ToNS(90000); got != 1_000_000_000 {
		t.Fatalf("ToNS %d", got)
	}
	if got := c.FromNS(1_000_000_000); got != 90000 {
		t.Fatalf("FromNS %d", got)
	}
	// Sub-tick conversion floors, never rounds up.
	if got := c.FromNS(1); got != 0 {
		t.Fatalf("FromNS(1)=%d", got)
	}
}

func TestSpanBoundariesAreHalfOpen(t *testing.T) {
	s, err := NewSpan(100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Contains(100) || s.Contains(200) {
		t.Fatal("half-open containment is wrong")
	}
	if s.ContainsSpan(Span{Start: 150, End: 200}) != true {
		t.Fatal("nested span must be contained")
	}
	if s.Overlaps(Span{Start: 200, End: 300}) {
		t.Fatal("touching spans must not overlap")
	}
	if _, ok := s.Intersect(Span{Start: 200, End: 300}); ok {
		t.Fatal("touching intersection must be empty")
	}
	if _, err := NewSpan(5, 4); err != ErrBadSpan {
		t.Fatalf("bad span error %v", err)
	}
}

func TestSegmentIndexPolicies(t *testing.T) {
	// Segment 0 = [0,100), segment 1 = [100,200), segment 2=[200,300).
	cases := []struct {
		t      int64
		policy BoundaryPolicy
		want   int64
	}{
		{0, EdgeOwnsRight, 0},
		{99, EdgeOwnsRight, 0},
		{100, EdgeOwnsRight, 1},
		{200, EdgeOwnsRight, 2},
		{100, EdgeOwnsLeft, 0},
		{200, EdgeOwnsLeft, 1},
		{250, EdgeOwnsLeft, 2},
		{-5, EdgeOwnsLeft, 0},
		{-5, EdgeOwnsRight, 0},
	}
	for _, c := range cases {
		got := SegmentIndex(c.t, 0, 100, c.policy)
		if got != c.want {
			t.Fatalf("SegmentIndex(%d,%v)=%d want %d", c.t, c.policy, got, c.want)
		}
	}
}

func TestSegmentIndexNonZeroOrigin(t *testing.T) {
	// Origin at 1000 with 100-tick segments.
	if got := SegmentIndex(1000, 1000, 100, EdgeOwnsRight); got != 0 {
		t.Fatalf("origin tick -> %d", got)
	}
	if got := SegmentIndex(1150, 1000, 100, EdgeOwnsRight); got != 1 {
		t.Fatalf("tick 1150 -> %d", got)
	}
	sp := SegmentSpan(2, 1000, 100)
	if sp.Start != 1200 || sp.End != 1300 {
		t.Fatalf("segment span %+v", sp)
	}
	if n := SegmentCount(1000, 1300, 1000, 100); n != 3 {
		t.Fatalf("segment count %d", n)
	}
}

func TestSampleCrossesBoundary(t *testing.T) {
	b := SampleBoundary{PTS: 95, DTS: 95, Dur: 10}
	if !b.Crosses(0, 100, EdgeOwnsRight) {
		t.Fatal("sample straddling 100 must cross")
	}
	b2 := SampleBoundary{PTS: 90, DTS: 90, Dur: 10}
	if b2.Crosses(0, 100, EdgeOwnsRight) {
		t.Fatal("sample ending exactly at 100 does not cross")
	}
	// Zero-duration samples still occupy one tick.
	b3 := SampleBoundary{PTS: 100, DTS: 100, Dur: 0}
	if b3.Footprint() != (Span{Start: 100, End: 101}) {
		t.Fatalf("footprint %+v", b3.Footprint())
	}
}

func TestMergeSpansAndGaps(t *testing.T) {
	// Zero-length spans cover no ticks and are dropped by MergeSpans.
	got := MergeSpans([]Span{{5, 10}, {1, 3}, {3, 6}, {12, 12}, {20, 21}})
	want := []Span{{1, 10}, {20, 21}}
	if len(got) != len(want) {
		t.Fatalf("merged %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged %+v want %+v", got, want)
		}
	}
	gaps := Gaps(0, 25, got)
	wantGaps := []Span{{0, 1}, {10, 20}, {21, 25}}
	if len(gaps) != len(wantGaps) {
		t.Fatalf("gaps %+v", gaps)
	}
	for i := range wantGaps {
		if gaps[i] != wantGaps[i] {
			t.Fatalf("gaps %+v want %+v", gaps, wantGaps)
		}
	}
}

func TestDedupeTimestampsKeepsFirst(t *testing.T) {
	kept, dropped := DedupeTimestamps([]int64{10, 20, 10, 30, 20, 40})
	if len(kept) != 4 || len(dropped) != 2 {
		t.Fatalf("kept=%v dropped=%v", kept, dropped)
	}
	if kept[0] != 0 || kept[1] != 1 || kept[2] != 3 || kept[3] != 5 {
		t.Fatalf("kept %v", kept)
	}
	if dropped[0] != 2 || dropped[1] != 4 {
		t.Fatalf("dropped %v", dropped)
	}
}

func TestNoFloatDriftOverManySteps(t *testing.T) {
	// Summing 29.97 fps ticks as floats drifts; exact rational scaling does
	// not. 30000/1001 is deliberately not representable in binary.
	const frames = 100000
	c, _ := NewClock(90000, 32)
	lastNS := int64(0)
	for i := 0; i < frames; i++ {
		ticks := int64(i) * 3003
		ns := c.ToNS(ticks)
		if ns < lastNS {
			t.Fatalf("non-monotonic at %d: %d < %d", i, ns, lastNS)
		}
		lastNS = ns
	}
	want, err := Scale(int64(frames-1)*3003, 90000, 1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if lastNS != want {
		t.Fatalf("drifted: %d want %d", lastNS, want)
	}
}
