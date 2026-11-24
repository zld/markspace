package clock

import (
	"math"
	"testing"
)

func TestRatArithmeticAndNormalization(t *testing.T) {
	a := NewRat(2, 4)
	if a.Num != 1 || a.Den != 2 {
		t.Fatalf("normalization failed: %v", a)
	}
	if got := NewRat(-1, -2); got.Num != 1 || got.Den != 2 {
		t.Fatalf("negative denominator not normalized: %v", got)
	}
	if FromMillis(1500).Cmp(NewRat(3, 2)) != 0 {
		t.Fatal("FromMillis mismatch")
	}
	if NewRat(1, 3).Add(NewRat(2, 3)).Cmp(NewRat(1, 1)) != 0 {
		t.Fatal("addition mismatch")
	}
	if NewRat(3, 4).Sub(NewRat(1, 4)).Cmp(NewRat(1, 2)) != 0 {
		t.Fatal("subtraction mismatch")
	}
	if NewRat(2, 3).Mul(NewRat(3, 4)).Cmp(NewRat(1, 2)) != 0 {
		t.Fatal("multiplication mismatch")
	}
	if got := NewRat(1, 3).RoundMillis(); got != 333 {
		t.Fatalf("RoundMillis = %d, want 333", got)
	}
}

func TestUnwrapperExpandsRollover(t *testing.T) {
	u := NewUnwrapper()
	// Walk up to just below 2^32 then cross the boundary.
	values := []uint32{0xFFFFFFF0, 0xFFFFFFF8, 0x00000004, 0x00000010}
	var got []int64
	for _, v := range values {
		exp, err := u.Expand(v)
		if err != nil {
			t.Fatalf("unexpected error at %#x: %v", v, err)
		}
		got = append(got, exp)
	}
	want := []int64{0xFFFFFFF0, 0xFFFFFFF8, 0x100000004, 0x100000010}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expanded[%d] = %d, want %d", i, got[i], want[i])
		}
	}
	wraps := u.Wraps()
	if len(wraps) != 1 || wraps[0].ToWrapped != 0x00000004 {
		t.Fatalf("wrap events = %+v", wraps)
	}
}

func TestUnwrapperRejectsNegativeJump(t *testing.T) {
	u := NewUnwrapper()
	if _, err := u.Expand(1_000_000); err != nil {
		t.Fatal(err)
	}
	// A jump backwards by far more than half a cycle cannot be a wrap.
	if _, err := u.Expand(1000); err == nil {
		t.Fatal("expected negative jump error")
	}
	if u.NegativeJumps() != 1 || u.LastNegativeSample() != 1 {
		t.Fatalf("jump accounting wrong: %d / %d", u.NegativeJumps(), u.LastNegativeSample())
	}
}

func TestTimelineMapping(t *testing.T) {
	tl := NewTimeline(90000).SetOrigin(90000)
	if tl.At(180000).Cmp(NewRat(1, 1)) != 0 {
		t.Fatalf("tick mapping = %v", tl.At(180000))
	}
	tl.SetDriftPPM(100)              // clock runs 100 ppm fast
	got := tl.Correct(90000 + 90000) // one raw second
	// Expected 1/1.0001 s -> 999900 microseconds.
	if got.Cmp(NewRat(999900, 1_000_000)) != 0 {
		t.Fatalf("corrected = %v", got)
	}
	if d := tl.CorrectionDelta(90000 + 90000); d.Seconds() >= 0 {
		t.Fatalf("fast clock correction delta should be negative: %v", d)
	}
}

func TestDriftEstimator(t *testing.T) {
	d := NewDriftEstimator(1000)
	// Source runs 120 ppm fast: 1000 source ticks advance while the
	// reference advances 0.99988 s.
	for i := 0; i <= 4; i++ {
		tick := int64(i * 1000)
		ref := NewRat(int64(float64(i)*999_880), 1_000_000)
		d.Add(Observation{RefSeconds: ref, Tick: tick})
	}
	ppm := d.EstimatePPM()
	if math.Abs(ppm-120) > 1 {
		t.Fatalf("estimated ppm = %f, want ~120", ppm)
	}
	res := d.Residuals(ppm)
	if len(res) != 5 {
		t.Fatalf("residual count = %d", len(res))
	}
	// Endpoints of an exact linear fit must have near-zero residuals.
	if math.Abs(float64(res[len(res)-1])) > 1000 {
		t.Fatalf("tail residual too large: %d", res[len(res)-1])
	}
}
