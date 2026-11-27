package clock

import "testing"

func TestNewNormalises(t *testing.T) {
	cases := []struct {
		num, den int64
		want     string
	}{
		{4000, 1000, "4"},
		{2, 4, "1/2"},
		{-2, -4, "1/2"},
		{2, -4, "-1/2"},
		{0, 7, "0"},
	}
	for _, c := range cases {
		got, err := New(c.num, c.den)
		if err != nil {
			t.Fatalf("New(%d,%d): %v", c.num, c.den, err)
		}
		if got.String() != c.want {
			t.Errorf("New(%d,%d)=%s want %s", c.num, c.den, got, c.want)
		}
	}
	if _, err := New(1, 0); err == nil {
		t.Fatal("expected zero denominator error")
	}
}

func TestExactAccumulation(t *testing.T) {
	// 1/3 second accumulated three times must be exactly 1 second, which a
	// float64 accumulator cannot guarantee.
	third := MustNew(1, 3)
	sum := Zero
	for i := 0; i < 3; i++ {
		sum = sum.Add(third)
	}
	if sum.Cmp(MustNew(1, 1)) != 0 {
		t.Fatalf("sum=%s want 1", sum)
	}
	if sum.Sub(third).Cmp(MustNew(2, 3)) != 0 {
		t.Fatalf("subtraction lost precision: %s", sum.Sub(third))
	}
}

func TestParseAndCompare(t *testing.T) {
	a, err := FromString("4.000")
	if err != nil {
		t.Fatal(err)
	}
	if a.String() != "4" {
		t.Fatalf("got %s", a)
	}
	b, err := FromString("9/2")
	if err != nil {
		t.Fatal(err)
	}
	if a.Cmp(b) >= 0 {
		t.Fatalf("4 should be less than 9/2")
	}
	if b.Abs().String() != "9/2" {
		t.Fatalf("abs=%s", b.Abs())
	}
	if _, err := FromString("x"); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := FromString(""); err == nil {
		t.Fatal("expected empty error")
	}
}

func TestFromTimescale(t *testing.T) {
	d, err := FromTimescale(90000*4, 90000)
	if err != nil || d.String() != "4" {
		t.Fatalf("got %s, %v", d, err)
	}
	if _, err := FromTimescale(1, 0); err == nil {
		t.Fatal("expected timescale error")
	}
	if got := MustNew(-3, 2).Seconds(); got != "-1.500" {
		t.Fatalf("seconds=%s", got)
	}
}
