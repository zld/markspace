package model

import "testing"

func TestRatArithmetic(t *testing.T) {
	r := MustRat(1, 90000) // one tick => seconds
	// 90000 ticks == 1 second, accumulated with exact rationals.
	var sum = Zero()
	for i := 0; i < 90000; i++ {
		v, err := sum.Add(r)
		if err != nil {
			t.Fatalf("add overflow at %d: %v", i, err)
		}
		sum = v
	}
	if sum.Cmp(MustRat(1, 1)) != 0 {
		t.Fatalf("90000 ticks = %v, want 1", sum)
	}
	// Half-away-from-zero rounding, no float drift.
	half := MustRat(1, 2)
	if got, err := half.ScaleInt(3); err != nil || got != 2 { // 3/2 -> 2
		t.Fatalf("ScaleInt(3) = %d, %v", got, err)
	}
	neg := MustRat(-1, 2)
	if got, err := neg.ScaleInt(3); err != nil || got != -2 {
		t.Fatalf("ScaleInt(-3/2) = %d, %v", got, err)
	}
	if c := MustRat(2, 3).Cmp(MustRat(3, 4)); c >= 0 {
		t.Fatalf("2/3 must be < 3/4, cmp=%d", c)
	}
	if _, err := NewRat(1, 0); err == nil {
		t.Fatal("zero denominator must error")
	}
}

func TestMulOverflow(t *testing.T) {
	if _, err := mul64(1<<62, 8); err == nil {
		t.Fatal("expected overflow")
	}
	if _, err := mul64(1<<62, 2); err == nil {
		t.Fatal("2^63 does not fit int64, expected overflow")
	}
	v, err := mul64(1<<61, 2)
	if err != nil || v != 1<<62 {
		t.Fatalf("2^61*2 = %d, %v", v, err)
	}
	if _, err := add64(1<<61, 1<<61); err != nil {
		t.Fatalf("2^62 must fit: %v", err)
	}
	if _, err := add64(1<<63-1, 1); err == nil {
		t.Fatal("expected add overflow")
	}
}

func TestErrorsSortedByOffset(t *testing.T) {
	var es Errors
	es.Newf(StageProbe, CodeTruncatedBox, "b.mp4", 0, ByteRange{Offset: 300, Length: 8}, "late")
	es.Newf(StageProbe, CodeIllegalLength, "a.mp4", 2, ByteRange{Offset: 10, Length: 4}, "early")
	es.Newf(StageProbe, CodeMissingTrack, "a.mp4", 0, ByteRange{Offset: 10, Length: 0}, "tie")
	sorted := es.Sorted()
	if sorted[0].ByteRange.Offset != 10 || sorted[2].ByteRange.Offset != 300 {
		t.Fatalf("bad ordering: %+v", sorted)
	}
	if es.Len() != 3 || es.Err() == nil {
		t.Fatal("Err() must be non-nil with 3 errors")
	}
	var empty Errors
	if empty.Err() != nil || empty.First() != nil {
		t.Fatal("empty collector must be nil")
	}
	if first := es.First(); first.ByteRange.Offset != 10 {
		t.Fatalf("first offset = %d", first.ByteRange.Offset)
	}
}
