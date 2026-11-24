package model

import "testing"

func TestTimestampConversionAndCompare(t *testing.T) {
	pa := TimeBase{Num: 1, Den: 48000}
	pb := TimeBase{Num: 1, Den: 96000}
	ts := NewTimestamp(480, pa) // 10ms
	got := ts.Convert(pb)
	if got.Ticks != 960 {
		t.Fatalf("convert = %d ticks, want 960", got.Ticks)
	}
	if got.Millis() != 10 {
		t.Fatalf("millis = %d, want 10", got.Millis())
	}
	a := Milliseconds(120)
	b := NewTimestamp(4800, TimeBase{Num: 1, Den: 40000}) // 120ms
	if a.Compare(b) != 0 {
		t.Fatalf("rational instants differ: %d vs %d", a.Millis(), b.Millis())
	}
	if !Milliseconds(9).Before(Milliseconds(10)) {
		t.Fatal("expected 9ms before 10ms")
	}
}

func TestTimeBaseRounding(t *testing.T) {
	base := TimeBase{Num: 1, Den: 30000}
	if got := base.Millis(150); got != 5 {
		t.Fatalf("150/30000 = %dms, want 5", got)
	}
	if got := base.Millis(1); got != 0 {
		t.Fatalf("round nearest = %dms, want 0", got)
	}
}

func TestMediaErrorCarriesContext(t *testing.T) {
	id := uint32(9)
	err := AtOffset(AtTrack(NewError(StageDemux, CodeTruncated, "short box", nil), id), 1234)
	if err.Error() == "" || err.SourceOffset == nil || *err.SourceOffset != 1234 {
		t.Fatalf("context missing: %#v", err)
	}
	if !HasCode(err, CodeTruncated) {
		t.Fatal("HasCode failed")
	}
}
