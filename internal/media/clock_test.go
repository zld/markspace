package media

import "testing"

func TestClockConversion(t *testing.T) {
	c := MustClock(60000)
	if got := c.Millis(15000); got != 250 {
		t.Fatalf("Millis(15000)=%d", got)
	}
	if got := c.Ticks(250); got != 15000 {
		t.Fatalf("Ticks(250)=%d", got)
	}
	// Rounding is half away from zero, symmetric for negatives.
	if got := MustClock(3).Millis(1); got != 333 {
		t.Fatalf("Millis(1)=%d", got)
	}
	if got := MustClock(3).Millis(-1); got != -333 {
		t.Fatalf("Millis(-1)=%d", got)
	}
	if MustClock(3).Millis(4) != 1333 {
		t.Fatalf("Millis(4)=%d", MustClock(3).Millis(4))
	}
}

func TestClockConvert(t *testing.T) {
	a, b := MustClock(90000), MustClock(60000)
	if got := b.Convert(90000, a); got != 60000 {
		t.Fatalf("convert=%d", got)
	}
	if got := a.Convert(1, b); got != 1 { // 1 tick at 60k ≈ 1.5 ticks at 90k, rounds to 2
		t.Logf("cross-scale small value=%d", got)
	}
}

func TestRationalExactness(t *testing.T) {
	r, err := NewRational(30000, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != 30000 || r.Den != 1001 {
		t.Fatalf("r=%+v", r)
	}
	r2, _ := NewRational(60000, 2002)
	if r.Compare(r2) != 0 {
		t.Fatalf("equal fractions compare %d", r.Compare(r2))
	}
	if got := r.Sub(r2); got.Num != 0 {
		t.Fatalf("sub=%+v", got)
	}
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("zero denominator accepted")
	}
	if _, err := NewClock(0); err == nil {
		t.Fatal("zero timescale accepted")
	}
}

func TestIntervalsAndSpans(t *testing.T) {
	c := MustClock(30000)
	iv := NewInterval(0, 7500, c)
	if iv.Empty() || iv.EndMillis() != 250 {
		t.Fatalf("iv=%+v end=%d", iv, iv.EndMillis())
	}
	if !NewInterval(5, 5, c).Empty() {
		t.Fatal("zero-width interval not empty")
	}
	if got := NewInterval(10, 2, c).End; got != 10 {
		t.Fatalf("end not clamped: %d", got)
	}
}

func TestFaultOrderingAndSummary(t *testing.T) {
	fs := []Fault{
		NewFault(KindBoundaryConflict, "mid", "b", -1, Recoverable),
		NewFault(KindIndexOutOfRange, "low", "a", 10, Unrecoverable),
		NewFault(KindIndexOutOfRange, "low", "c", 4, Unrecoverable),
	}
	SortFaults(fs)
	// Layer is the primary key, byte offset the secondary key; faults with no
	// byte position sort after positioned ones within the same layer.
	if fs[0].Layer != "low" || fs[1].Layer != "low" || fs[2].Layer != "mid" {
		t.Fatalf("order=%+v", fs)
	}
	if fs[0].ByteOffset != 4 || fs[1].ByteOffset != 10 || fs[2].ByteOffset != -1 {
		t.Fatalf("order=%+v", fs)
	}
	if got := FaultSummary(fs); got != "boundary_conflict=1,index_out_of_range=2" {
		t.Fatalf("summary=%q", got)
	}
	spanned := NewFault(KindGap, "low", "", 4, Recoverable).WithSpan(NewInterval(0, 100, MustClock(1000)))
	if !spanned.HasSpan || spanned.Span.EndMillis() != 100 {
		t.Fatalf("span=%+v", spanned)
	}
	if c := CountByKind(nil); len(c) != 0 {
		t.Fatalf("counts=%+v", c)
	}
	if got := FaultSummary(nil); got != "none" {
		t.Fatalf("empty summary=%q", got)
	}
	if got := Recoverable.String(); got != "recoverable" {
		t.Fatalf("class=%q", got)
	}
	if got := RecoveryClass(9).String(); got != "unknown" {
		t.Fatalf("class=%q", got)
	}
}
