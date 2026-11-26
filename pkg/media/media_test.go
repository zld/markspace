package media

import (
	"errors"
	"testing"
	"time"
)

func TestTimebaseTicksAndOverflow(t *testing.T) {
	tb := Timebase{Timescale: 3000}
	ticks, err := tb.Ticks(time.Second)
	if err != nil || ticks != 3000 {
		t.Fatalf("Ticks(time.Second)=%d,%v", ticks, err)
	}
	if d := tb.Duration(3000); d != time.Second {
		t.Fatalf("Duration=%s", d)
	}
	if _, err := (Timebase{}).Ticks(time.Second); err == nil {
		t.Fatal("zero timescale must fail")
	}
	largeTB := Timebase{Timescale: ^uint32(0)}
	if _, err := largeTB.Ticks(time.Duration(1 << 62)); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestClockPTSAndAdvance(t *testing.T) {
	c := Clock{Timebase: Timebase{Timescale: 3000}}
	pts, err := c.PTS(9000, -3000)
	if err != nil || pts != 6000 {
		t.Fatalf("PTS=%d,%v", pts, err)
	}
	if _, err := c.PTS(1<<62, 1<<62); err == nil {
		t.Fatal("expected PTS overflow")
	}
	next, err := Advance(6000, 3000)
	if err != nil || next != 9000 {
		t.Fatalf("Advance=%d,%v", next, err)
	}
	if _, err := Advance(1<<63-1, 1); err == nil {
		t.Fatal("expected advance overflow")
	}
}

func TestFaultStructuredFieldsAndUnwrap(t *testing.T) {
	cause := errors.New("boom")
	f := NewFault(ClassBadSTCO, SeverityError, "out",
		WithBox("stco", 42), WithTrack(7), WithScope(ScopeTrack), WithCause(cause))
	if f.Offset != 42 || f.TrackID != 7 || f.RecomputeScope != ScopeTrack {
		t.Fatalf("fault fields wrong: %+v", f)
	}
	if !errors.Is(f, cause) {
		t.Fatal("fault must unwrap cause")
	}
	r := &Report{}
	r.Add(f)
	if !r.HasClass(ClassBadSTCO) || !r.Corrupt() || len(r.ByClass(ClassBadSTCO)) != 1 {
		t.Fatalf("report=%+v", r)
	}
	if got, ok := AsFault(f); !ok || got.Class != ClassBadSTCO {
		t.Fatalf("AsFault=%v,%v", got, ok)
	}
}
