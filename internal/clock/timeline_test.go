package clock

import (
	"errors"
	"math"
	"testing"
)

func TestMakeRejectsBadDenominator(t *testing.T) {
	if _, err := Make(1, 0); !errors.Is(err, ErrOverflow) {
		t.Fatalf("zero denominator: want ErrOverflow, got %v", err)
	}
	if _, err := Make(-1, 2); !errors.Is(err, ErrOverflow) {
		t.Fatalf("negative numerator: want ErrOverflow, got %v", err)
	}
}

func TestMakeReducesFraction(t *testing.T) {
	r, err := Make(6, 8)
	if err != nil {
		t.Fatal(err)
	}
	if r.N != 3 || r.D != 4 {
		t.Fatalf("got %d/%d, want 3/4", r.N, r.D)
	}
}

func TestCmpExact(t *testing.T) {
	cases := []struct {
		a, b Rational
		want int
	}{
		{MustMake(1, 3), MustMake(2, 3), -1},
		{MustMake(2, 3), MustMake(1, 3), 1},
		{MustMake(1, 3), MustMake(2, 6), 0},
		// Different timescales, same value: 90000/90000 == 25/25.
		{MustMake(90000, 90000), MustMake(25, 25), 0},
		// Large cross product that a float compare could round.
		{MustMake(1_000_000_001, 1_000_000_000), MustMake(1_000_000_000, 999_999_999), -1},
	}
	for i, c := range cases {
		if got := c.a.Cmp(c.b); got != c.want {
			t.Fatalf("case %d: Cmp=%d want %d", i, got, c.want)
		}
	}
}

func TestAddSub(t *testing.T) {
	a := MustMake(1, 25)
	b := MustMake(1, 25)
	sum, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Cmp(MustMake(2, 25)) != 0 {
		t.Fatalf("sum = %s", sum)
	}
	back, err := sum.Sub(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.Cmp(a) != 0 {
		t.Fatalf("sub = %s", back)
	}
	if _, err := a.Sub(MustMake(2, 1)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("negative sub: want overflow, got %v", err)
	}
}

func TestTicksExprHalfUp(t *testing.T) {
	r := MustMake(1, 3)
	got, err := r.TicksExpr(10)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 { // 10/3 = 3.333... rounded down
		t.Fatalf("got %d, want 3", got)
	}
	r2 := MustMake(2, 3)
	got2, err := r2.TicksExpr(10)
	if err != nil {
		t.Fatal(err)
	}
	if got2 != 7 { // 6.666... half-up -> 7
		t.Fatalf("got %d, want 7", got2)
	}
}

func TestFromTicksRejectsZeroTimescale(t *testing.T) {
	if _, err := FromTicks(5, 0); !errors.Is(err, ErrOverflow) {
		t.Fatalf("got %v", err)
	}
}

func TestMillisecondsIsReportOnly(t *testing.T) {
	r := MustMake(1, 25)
	if math.Abs(r.Milliseconds()-40.0) > 1e-9 {
		t.Fatalf("ms = %f", r.Milliseconds())
	}
}

func TestErrorWrapAndContext(t *testing.T) {
	err := NewError(StageDemux, "open", ErrCorrupt).WithTrack(2).WithSample(7)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatal("errors.Is lost sentinel")
	}
	msg := err.Error()
	for _, want := range []string{"stage=demux", "track=2", "sample=7", "op=open"} {
		if !contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
	}
	// Re-wrap keeps the original stage context instead of nesting forever.
	again := Wrap(StageDemux, "retry", err)
	if again.(*Error).Sample != 7 {
		t.Fatalf("rewrap lost sample: %v", again)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
