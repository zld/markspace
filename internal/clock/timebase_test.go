package clock

import (
	"errors"
	"testing"
)

func TestNewNormalizes(t *testing.T) {
	r := MustNew(48000 * 2, 48000)
	if r.Num != 2 || r.Den != 1 {
		t.Fatalf("got %s want 2/1", r)
	}
	r = MustNew(1, -100)
	if r.Num != -1 || r.Den != 100 {
		t.Fatalf("got %s want -1/100", r)
	}
	if _, err := New(1, 0); err == nil {
		t.Fatal("zero denominator must fail")
	}
}

func TestConvertTicksExact(t *testing.T) {
	cases := []struct {
		ticks int64
		from  Rational
		to    Rational
		want  int64
	}{
		{48000, MustNew(1, 48000), Master, 90000},
		{90000, Master, MustNew(1, 48000), 48000},
		{25, MustNew(1, 25), Master, 3600},
		{0, MustNew(1, 48000), Master, 0},
		{1001, MustNew(1, 24000), Master, 3754}, // 3753.75 rounds up
	}
	for _, c := range cases {
		got, err := ConvertTicks(c.ticks, c.from, c.to)
		if err != nil {
			t.Fatalf("%d %s->%s: %v", c.ticks, c.from, c.to, err)
		}
		if got != c.want {
			t.Fatalf("%d %s->%s: got %d want %d", c.ticks, c.from, c.to, got, c.want)
		}
	}
}

func TestConvertTicksRoundsHalfAwayFromZero(t *testing.T) {
	from := MustNew(1, 2)
	to := MustNew(1, 1)
	got, err := ConvertTicks(1, from, to) // 0.5 -> 1
	if err != nil || got != 1 {
		t.Fatalf("half up: got %d err %v", got, err)
	}
	if _, err := ConvertTicks(1, Rational{Num: 1, Den: 0}, to); err == nil {
		t.Fatal("invalid base must fail")
	}
}

func TestConvertTicksDeterministic(t *testing.T) {
	from := MustNew(1001, 30000)
	for i := int64(0); i < 1000; i++ {
		a, err := ConvertTicks(i*37, from, Master)
		if err != nil {
			t.Fatal(err)
		}
		b, err := ConvertTicks(i*37, from, Master)
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatalf("non-deterministic at %d: %d != %d", i, a, b)
		}
	}
}

func TestTimeAddSub(t *testing.T) {
	a, _ := NewTime(1, MustNew(1, 1000))
	b, _ := NewTime(500, MustNew(1, 500))
	sum, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Ticks != 1001 {
		t.Fatalf("got %d want 1001 ticks in ms base", sum.Ticks)
	}
	diff, err := sum.Sub(a)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Ticks != 1000 {
		t.Fatalf("got %d want 1000", diff.Ticks)
	}
	if _, err := a.Add(Time{Ticks: 1 << 62, Base: Master}); err != nil {
		t.Fatalf("unexpected overflow: %v", err)
	}
}

func TestFromFrames(t *testing.T) {
	tm, err := FromFrames(48000, 48000, Master)
	if err != nil {
		t.Fatal(err)
	}
	if tm.Ticks != 90000 {
		t.Fatalf("got %d want 90000", tm.Ticks)
	}
	if _, err := FromFrames(1, 0, Master); err == nil {
		t.Fatal("zero sample rate must fail")
	}
}

func TestCmp(t *testing.T) {
	a := MustNew(1, 3)
	b := MustNew(2, 6)
	if a.Cmp(b) != 0 {
		t.Fatal("1/3 must equal 2/6")
	}
	if Master.Cmp(MustNew(1, 2)) <= 0 {
		t.Fatal("90000 should exceed 1/2")
	}
}

func TestErrorNotSwallowed(t *testing.T) {
	_, err := ConvertTicks(1, Master, Rational{Num: 0, Den: 1})
	if err == nil || errors.Is(err, nil) {
		t.Fatalf("expected conversion error, got %v", err)
	}
}
