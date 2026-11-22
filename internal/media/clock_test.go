package media

import (
	"math"
	"testing"
)

func TestNewTimebaseRejectsZero(t *testing.T) {
	_, err := NewTimebase(0)
	if err == nil {
		t.Fatal("expected error for zero timescale")
	}
	if CategoryOf(err) != ZeroTimescale {
		t.Fatalf("category = %v, want %v", CategoryOf(err), ZeroTimescale)
	}
}

func TestToNanosExactValues(t *testing.T) {
	cases := []struct {
		value     int64
		timescale uint32
		want      Nano
	}{
		{0, 1000, 0},
		{1, 1000, 1_000_000},
		{1500, 30000, 50_000_000},
		{1, 90000, 11_111}, // 11111.111 -> rounds up
		{2, 90000, 22_222}, // 22222.222 -> rounds down
		{90000, 90000, 1_000_000_000},
		{-1, 1000, -1_000_000},
	}
	for _, c := range cases {
		got, err := ToNanos(c.value, c.timescale)
		if err != nil {
			t.Fatalf("ToNanos(%d,%d): %v", c.value, c.timescale, err)
		}
		if got != c.want {
			t.Errorf("ToNanos(%d,%d) = %d, want %d", c.value, c.timescale, got, c.want)
		}
	}
}

func TestToNanosRejectsOverflow(t *testing.T) {
	_, err := ToNanos(math.MaxInt64, 1)
	if err == nil || CategoryOf(err) != Overflow {
		t.Fatalf("expected overflow, got %v", err)
	}
	_, err = ToNanos(math.MinInt64, 1)
	if err == nil || CategoryOf(err) != Overflow {
		t.Fatalf("expected overflow for MinInt64, got %v", err)
	}
}

func TestDurationRejectsNegative(t *testing.T) {
	tb, _ := NewTimebase(1000)
	_, err := tb.Duration(-5)
	if err == nil || CategoryOf(err) != NegativeDuration {
		t.Fatalf("expected negative duration error, got %v", err)
	}
}

func TestRoundingIsDeterministicAndSymmetric(t *testing.T) {
	for _, v := range []int64{1, 2, 3, 7, 11, 12345} {
		pos, err := ToNanos(v, 90000)
		if err != nil {
			t.Fatal(err)
		}
		neg, err := ToNanos(-v, 90000)
		if err != nil {
			t.Fatal(err)
		}
		if pos != -neg {
			t.Errorf("asymmetric rounding: ToNanos(%d)=%d ToNanos(%d)=%d", v, pos, -v, neg)
		}
	}
}

func TestToTicksRoundTrip(t *testing.T) {
	for _, ts := range []uint32{600, 1000, 90000, 48000} {
		for _, tick := range []int64{0, 1, 100, 90000} {
			ns, err := ToNanos(tick, ts)
			if err != nil {
				t.Fatal(err)
			}
			back, err := ToTicks(ns, ts)
			if err != nil {
				t.Fatal(err)
			}
			if back != tick {
				t.Errorf("ts=%d tick=%d round-trip = %d", ts, tick, back)
			}
		}
	}
}

func TestAddSubDetectOverflow(t *testing.T) {
	if _, err := Add(Nano(math.MaxInt64), 1); err == nil {
		t.Fatal("Add should overflow")
	}
	if _, err := Sub(Nano(math.MinInt64), 1); err == nil {
		t.Fatal("Sub should overflow")
	}
	if got, err := Add(5, -3); err != nil || got != 2 {
		t.Fatalf("Add(5,-3) = %d, %v", got, err)
	}
}

func TestErrorFormattingIncludesLocation(t *testing.T) {
	err := NewError(CorruptSampleTable, "bad stts").AtTrack(2).AtOffset(1024)
	got := err.Error()
	for _, want := range []string{"corrupt_sample_table", "track=2", "offset=1024", "bad stts"} {
		if !contains(got, want) {
			t.Errorf("error %q missing %q", got, want)
		}
	}
	if !IsCorruptInput(err) {
		t.Error("corrupt sample table should classify as corrupt input")
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
