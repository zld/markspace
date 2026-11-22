package media

import (
	"errors"
	"math"
	"testing"
)

func TestRatNormalization(t *testing.T) {
	r, err := NewRat(6, -8)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != -3 || r.Den != 4 {
		t.Fatalf("normalized %v", r)
	}
	if _, err := NewRat(1, 0); err == nil {
		t.Fatal("zero denominator must fail")
	} else if err.(*Error).Kind != KindOverflow {
		t.Fatalf("kind %v", err)
	}
}

func TestRatAddMulOverflow(t *testing.T) {
	big := Rat{Num: math.MaxInt64, Den: 1}
	if _, err := big.Add(Rat{Num: math.MaxInt64, Den: 1}); err == nil {
		t.Fatal("add overflow must fail")
	} else if err.(*Error).Kind != KindOverflow {
		t.Fatalf("kind %v", err)
	}
	if _, err := big.Mul(Rat{Num: math.MaxInt64, Den: 1}); err == nil {
		t.Fatal("mul overflow must fail")
	}
	// Cross-cancellation keeps a legal product representable.
	a := Rat{Num: 1 << 40, Den: 3}
	b := Rat{Num: 3, Den: 1 << 31}
	p, err := a.Mul(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.Num != 1<<9 || p.Den != 1 {
		t.Fatalf("product %v", p)
	}
}

func TestRatCmp(t *testing.T) {
	cases := []struct {
		a, b Rat
		want int
	}{
		{Rat{1, 2}, Rat{1, 3}, 1},
		{Rat{-1, 2}, Rat{1, 3}, -1},
		{Rat{2, 4}, Rat{1, 2}, 0},
		{Rat{math.MinInt64, 1}, Rat{math.MaxInt64, 1}, -1},
	}
	for _, c := range cases {
		if got := c.a.Cmp(c.b); got != c.want {
			t.Errorf("%v Cmp %v = %d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestClockRoundTrip(t *testing.T) {
	c, err := NewClock(90000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ticks := range []int64{0, 1, 3000, 2999, -3000, 1 << 40} {
		s, err := c.ToSeconds(ticks)
		if err != nil {
			t.Fatal(err)
		}
		back, err := c.Ticks(s)
		if err != nil {
			t.Fatal(err)
		}
		if back != ticks {
			t.Errorf("ticks %d round trip %d", ticks, back)
		}
	}
	if _, err := NewClock(0); err == nil {
		t.Fatal("zero timescale must fail")
	}
	if _, err := NewClock(-5); err == nil {
		t.Fatal("negative timescale must fail")
	}
}

func TestClockTicksRoundsNearest(t *testing.T) {
	c, _ := NewClock(10)
	// 1/3 second at 10 ticks/s is 3.33 -> 3; 2/3 is 6.67 -> 7.
	if got, _ := c.Ticks(Rat{Num: 1, Den: 3}); got != 3 {
		t.Fatalf("1/3 -> %d", got)
	}
	if got, _ := c.Ticks(Rat{Num: 2, Den: 3}); got != 7 {
		t.Fatalf("2/3 -> %d", got)
	}
}

func TestErrorContext(t *testing.T) {
	err := NewError(KindChecksum, 3, 42, 1024, "bad sum")
	var me *Error
	if !errors.As(err, &me) {
		t.Fatal("not a *Error")
	}
	if me.Track != 3 || me.Seq != 42 || me.Offset != 1024 || me.Kind != KindChecksum {
		t.Fatalf("context lost: %+v", me)
	}
	if me.Error() == "" {
		t.Fatal("empty message")
	}
}

func TestFrameCloneIsDeep(t *testing.T) {
	f := &Frame{StreamID: 1, Seq: 9, Payload: []byte{1, 2, 3}}
	f.Sum = f.ComputeSum()
	c := f.Clone()
	c.Payload[0] = 99
	if f.Payload[0] == 99 {
		t.Fatal("clone shares payload storage")
	}
	if c.Sum != f.Sum || c.Seq != f.Seq {
		t.Fatal("clone lost identity")
	}
}

func TestFrameStateStrings(t *testing.T) {
	for st, want := range map[FrameState]string{
		StateClean: "clean", StateRecovered: "recovered",
		StatePlaceholder: "placeholder", StateDegraded: "degraded",
		FrameState(99): "unknown",
	} {
		if st.String() != want {
			t.Errorf("%d -> %q want %q", st, st.String(), want)
		}
	}
}
