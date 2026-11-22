package timeline

import "testing"

func TestCursorCoversWholeRateConversionsExactly(t *testing.T) {
	cases := []struct{ in, out, inRate, outRate int64 }{
		{480, 480, 48000, 48000},
		{441, 480, 44100, 48000},
		{1024, 1024, 96000, 96000},
		{960, 800, 48000, 40000},
	}
	for _, c := range cases {
		got, err := CursorCount(c.in, c.inRate, c.outRate)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.out {
			t.Errorf("%d@%d -> %d@%d: got %d output samples, want %d", c.in, c.inRate, c.outRate, c.out, got, c.out)
		}
	}
}

func TestCursorPhaseHasNoAccumulatedDrift(t *testing.T) {
	c, err := NewSampleCursor(44100, 48000, 0, 0, 4800)
	if err != nil {
		t.Fatal(err)
	}
	for n := int64(0); n < c.Count(); n++ {
		p, err := c.At(n)
		if err != nil {
			t.Fatal(err)
		}
		// n*44100/48000 must lie exactly on the reported base+remainder.
		if int64(p.Base)*c.OutRate+int64(p.Remainder) != n*c.InRate {
			t.Fatalf("phase drift at %d: %+v", n, p)
		}
		if p.Remainder < 0 || p.Remainder >= c.OutRate {
			t.Fatalf("remainder out of range at %d: %d", n, p.Remainder)
		}
	}
}

func TestCursorTailAndBounds(t *testing.T) {
	// Odd input length: 1023 samples at 44.1k -> 1113.something at 48k.
	n, err := CursorCount(1023, 44100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1113 {
		t.Fatalf("tail count = %d, want 1113", n)
	}
	c, err := NewSampleCursor(44100, 48000, 0, 0, n)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.At(n); err == nil {
		t.Fatal("index at count accepted")
	}
	if _, err := c.At(-1); err == nil {
		t.Fatal("negative index accepted")
	}
	last, err := c.SourcePosition(n - 1)
	if err != nil {
		t.Fatal(err)
	}
	if last > 1023 {
		t.Fatalf("source position %d exceeds input", last)
	}
}

func TestCursorCountRejectsBadRates(t *testing.T) {
	if _, err := CursorCount(10, 0, 48000); err == nil {
		t.Fatal("zero input rate accepted")
	}
	if _, err := NewSampleCursor(48000, 48000, 0, 0, -1); err == nil {
		t.Fatal("negative count accepted")
	}
}
