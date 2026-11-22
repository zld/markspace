package clock

import "testing"

func TestIntervalOps(t *testing.T) {
	a := Interval{Start: 0, End: 100}
	b := Interval{Start: 100, End: 200}
	c := Interval{Start: 50, End: 150}
	if !a.Abuts(b) {
		t.Fatal("a should abut b")
	}
	if a.Overlaps(b) {
		t.Fatal("abutting half-open intervals must not overlap")
	}
	if !a.Overlaps(c) || !c.Overlaps(b) {
		t.Fatal("expected overlap")
	}
	if !a.Contains(99) || a.Contains(100) {
		t.Fatal("half-open containment wrong")
	}
	if a.Dur() != 100 || !(Interval{5, 5}).Empty() {
		t.Fatal("duration/empty wrong")
	}
}

func TestMsRoundTrip(t *testing.T) {
	if got := Ms(FromMs(20)); got != 20 {
		t.Fatalf("round trip drift: %v", got)
	}
	if FromSeconds(1.5) != 72000 {
		t.Fatalf("FromSeconds wrong: %d", FromSeconds(1.5))
	}
}

func TestSeq16Wrap(t *testing.T) {
	cases := []struct {
		a, b uint16
		want int32
	}{
		{0, 65535, 1},     // wrap forward
		{65535, 0, -1},    // wrap backward
		{10, 5, 5},        // plain
		{5, 65530, 11},    // near wrap
		{0, 32768, -32768},// max negative lag
		{32768, 0, -32768}, // half-range maps to negative by convention
	}
	for _, c := range cases {
		if got := Seq16Delta(c.a, c.b); got != c.want {
			t.Fatalf("Seq16Delta(%d,%d)=%d want %d", c.a, c.b, got, c.want)
		}
	}
	if !Seq16Less(65535, 0) {
		t.Fatal("65535 must precede 0 across wrap")
	}
	if Seq16Less(0, 65535) {
		t.Fatal("0 must not precede 65535")
	}
}

func TestAccumulatorDrift(t *testing.T) {
	// 44100 -> 48000 over many small chunks: cumulative error must stay
	// below one frame versus the theoretical value.
	acc := NewAccumulator(44100, 48000)
	var totalOut int64
	var totalIn int64
	for i := 0; i < 1000; i++ {
		n := acc.Next(100)
		totalOut += n
		totalIn += 100
		theory := totalIn * 48000 / 44100
		if d := theory - totalOut; d < 0 || d > 0 {
			t.Fatalf("drift at step %d: got %d want %d", i, totalOut, theory)
		}
	}
	if acc.Expected() != totalOut {
		t.Fatalf("final mismatch: %d vs %d", totalOut, acc.Expected())
	}
}

func TestAccumulatorBoundary(t *testing.T) {
	acc := NewAccumulator(48000, 48000)
	if n := acc.Next(0); n != 0 {
		t.Fatalf("zero input must yield zero output, got %d", n)
	}
	if n := acc.Next(1); n != 1 {
		t.Fatalf("identity rate must be exact, got %d", n)
	}
	// Downsample 48000 -> 8000: 7 frames in -> 1 out (floor 7/6=1).
	d := NewAccumulator(48000, 8000)
	if n := d.Next(7); n != 1 {
		t.Fatalf("downsample boundary wrong: %d", n)
	}
}

func TestStageError(t *testing.T) {
	err := &StageError{Track: "t1", Stage: StageIngest, Op: "validate", Err: errTest{}}
	want := "track=t1 stage=ingest op=validate: boom"
	if err.Error() != want {
		t.Fatalf("got %q want %q", err.Error(), want)
	}
}

type errTest struct{}

func (errTest) Error() string { return "boom" }
