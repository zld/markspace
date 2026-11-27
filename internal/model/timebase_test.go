package model

import (
	"errors"
	"testing"
)

func TestSampleConversionsRoundTrip(t *testing.T) {
	const rate = uint64(48000)
	for _, samples := range []uint64{0, 1, 48, 480, 48000, 1234567} {
		ns := SamplesToNS(samples, rate)
		back := NSToSamples(ns, rate)
		if back != samples {
			t.Fatalf("sample round trip %d -> %dns -> %d", samples, ns, back)
		}
	}
	if got := SamplesToNS(48000, rate); got != 1_000_000_000 {
		t.Fatalf("one second = %d ns, want 1e9", got)
	}
}

func TestRTPConversionsRoundTrip(t *testing.T) {
	const clock = uint64(90000)
	for _, ticks := range []uint64{0, 1, 90, 90000, 7654321} {
		ns := RTPToNS(ticks, clock)
		if back := NSToRTP(ns, clock); back != ticks {
			t.Fatalf("rtp round trip %d -> %dns -> %d", ticks, ns, back)
		}
	}
}

func TestWrapDiff(t *testing.T) {
	cases := []struct {
		a, b uint32
		want int64
	}{
		{100, 200, 100},
		{200, 100, -100},
		{0xFFFFFFFF, 5, 6},          // legitimate wrap forward
		{5, 0xFFFFFFFF, -6},         // backwards across the ring
		{0x80000000, 0, 2147483648}, // exact half ring stays positive by convention
		{0x80000001, 0, 2147483647}, // just inside the backward half
	}
	for _, c := range cases {
		if got := WrapDiff(c.a, c.b); got != c.want {
			t.Fatalf("WrapDiff(%d,%d)=%d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRangeOverlapAndMerge(t *testing.T) {
	if (Range{0, 10}).Overlaps(Range{10, 20}) {
		t.Fatal("half-open ranges touching at a boundary must not overlap")
	}
	if !(Range{0, 11}).Overlaps(Range{10, 20}) {
		t.Fatal("ranges sharing element 10 must overlap")
	}
	if got := (Range{0, 10}).Merge(Range{5, 20}); got != (Range{0, 20}) {
		t.Fatalf("merge = %v", got)
	}
}

func TestStructuredError(t *testing.T) {
	base := errors.New("io: short read")
	se := NewStructuredError(StageWAV, ReasonHeader, 12, 0, "bad riff", "reject input", base)
	if !errors.Is(se, base) {
		t.Fatal("Unwrap must expose the cause")
	}
	if se.Error() == "" || se.Anomaly.Offset != 12 {
		t.Fatal("structured error must render and keep its fields")
	}
	if !Fatal(ReasonLimit) || Fatal(ReasonTruncatedBlock) {
		t.Fatal("Fatal classification mismatch")
	}
}
