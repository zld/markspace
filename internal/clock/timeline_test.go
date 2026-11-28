package clock

import "testing"

func TestToTicksFloorAndError(t *testing.T) {
	// 48000 Hz -> 90000 Hz: 48000 source units map to 90000 ticks.
	c, err := NewConverter(48000)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ToTicks(48000); got != TicksPerSecond {
		t.Fatalf("1s = %d ticks, want %d", got, TicksPerSecond)
	}
	// 1 source unit: 90000/48000 = 1.875 -> floor 1 tick, remainder 42000.
	if got := c.ToTicks(1); got != 1 {
		t.Fatalf("floor conversion = %d, want 1", got)
	}
	ae := c.AlignmentError()
	if ae.MaxErrorTicks > 1 {
		t.Fatalf("max alignment error %d ticks, must be <= 1", ae.MaxErrorTicks)
	}
	if ae.WorstRemainder != 42000 || ae.WorstDenom != 48000 {
		t.Fatalf("worst remainder = (%d/%d), want (42000/48000)",
			ae.WorstRemainder, ae.WorstDenom)
	}
}

func TestToTicksNonDivisibleStaysUnderOneTick(t *testing.T) {
	c, _ := NewConverter(44100)
	// Accumulate via absolute times: error per boundary must stay < 1 tick.
	for src := int64(1); src < 44100; src++ {
		ticks := c.ToTicks(src)
		num := src * TicksPerSecond
		want := num / 44100
		if ticks != want {
			t.Fatalf("src=%d ticks=%d want=%d", src, ticks, want)
		}
	}
	if c.AlignmentError().MaxErrorTicks > 1 {
		t.Fatal("alignment error exceeds 1 tick")
	}
}

func TestZeroTimescaleRejected(t *testing.T) {
	if _, err := NewConverter(0); err == nil {
		t.Fatal("zero timescale must fail")
	}
}

func TestSortPointsStable(t *testing.T) {
	pts := []TimelinePoint{
		{Tick: 10, TrackIndex: 2, SampleIndex: 0},
		{Tick: 10, TrackIndex: 1, SampleIndex: 5},
		{Tick: 5, TrackIndex: 0, SampleIndex: 9},
		{Tick: 10, TrackIndex: 1, SampleIndex: 2},
	}
	SortPoints(pts)
	want := []TimelinePoint{
		{5, 0, 9},
		{10, 1, 2},
		{10, 1, 5},
		{10, 2, 0},
	}
	for i := range want {
		if pts[i] != want[i] {
			t.Fatalf("pos %d = %+v, want %+v", i, pts[i], want[i])
		}
	}
}

func TestMergeMax(t *testing.T) {
	got := MergeMax(
		AlignmentError{WorstRemainder: 3},
		AlignmentError{WorstRemainder: 9, MaxErrorTicks: 1},
		AlignmentError{WorstRemainder: 2},
	)
	if got.WorstRemainder != 9 {
		t.Fatalf("merge picked remainder %d, want 9", got.WorstRemainder)
	}
}
