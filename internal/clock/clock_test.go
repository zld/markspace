package clock

import (
	"errors"
	"math"
	"testing"

	"tracecheck/internal/serr"
)

func TestTicksToNanos(t *testing.T) {
	tb := Timebase{TickNanos: NanosPerMilli, ClientUnitNanos: NanosPerMilli}
	ns, err := tb.TicksToNanos(1234)
	if err != nil {
		t.Fatal(err)
	}
	if ns != 1234*NanosPerMilli {
		t.Fatalf("got %d", ns)
	}
	// A negative relative timecode is legal in Matroska.
	ns, err = tb.TicksToNanos(-3)
	if err != nil || ns != -3*NanosPerMilli {
		t.Fatalf("negative ticks: %d %v", ns, err)
	}
	if _, err := tb.TicksToNanos(math.MaxInt64); err == nil ||
		serr.CodeOf(err) != serr.CodeClockOverflow {
		t.Fatalf("expected overflow, got %v", err)
	}
}

func TestUnitConversions(t *testing.T) {
	if ns, err := MicrosToNanos(1500); err != nil || ns != 1_500_000 {
		t.Fatalf("micros: %d %v", ns, err)
	}
	if ns, err := MillisToNanos(2); err != nil || ns != 2_000_000 {
		t.Fatalf("millis: %d %v", ns, err)
	}
	if _, err := MillisToNanos(math.MaxInt64); err == nil {
		t.Fatal("expected millisecond overflow")
	}
}

func TestClientToNanosOverflow(t *testing.T) {
	tb := Timebase{TickNanos: 1, ClientUnitNanos: NanosPerSecond}
	if _, err := tb.ClientToNanos(math.MaxInt64); err == nil ||
		serr.CodeOf(err) != serr.CodeClockOverflow {
		t.Fatalf("expected overflow, got %v", err)
	}
	if _, err := tb.ClientToNanos(math.MinInt64); err == nil {
		t.Fatal("expected negative overflow")
	}
}

func TestMapperInterpolation(t *testing.T) {
	tb := DefaultTimebase()
	m, err := NewMapper(tb, []Anchor{
		{ClientNanos: 0, MediaNanos: 1_000_000_000},
		{ClientNanos: 2 * NanosPerSecond, MediaNanos: 3_000_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := m.Map(1000, "client-log") // 1000 ms
	if err != nil {
		t.Fatal(err)
	}
	if ev.Raw != 1000 || ev.ClientNanos != NanosPerSecond {
		t.Fatalf("raw/client: %+v", ev)
	}
	if ev.MediaNanos != 2*NanosPerSecond {
		t.Fatalf("media = %d, want 2s", ev.MediaNanos)
	}
	if ev.ErrorNanos != NanosPerSecond {
		t.Errorf("error = %d, want 1s", ev.ErrorNanos)
	}
}

func TestMapperExactAnchor(t *testing.T) {
	m, _ := NewMapper(DefaultTimebase(), []Anchor{{ClientNanos: 5, MediaNanos: 9}})
	ev, err := m.MapNanos(5, "x")
	if err != nil {
		t.Fatal(err)
	}
	if ev.MediaNanos != 9 || ev.ErrorNanos != 0 {
		t.Fatalf("anchor hit: %+v", ev)
	}
}

func TestMapperNoAnchor(t *testing.T) {
	m, err := NewMapper(DefaultTimebase(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Map(10, "client-log")
	if serr.CodeOf(err) != serr.CodeNoAnchor {
		t.Fatalf("want NO_ANCHOR, got %v", err)
	}
}

func TestMapperExtrapolationLimit(t *testing.T) {
	m, _ := NewMapper(DefaultTimebase(), []Anchor{
		{ClientNanos: 10 * NanosPerSecond, MediaNanos: 10 * NanosPerSecond},
		{ClientNanos: 20 * NanosPerSecond, MediaNanos: 20 * NanosPerSecond},
	})
	m.SetMaxExtrapolation(NanosPerSecond)
	if _, err := m.Map(5, "x"); serr.CodeOf(err) != serr.CodeNoAnchor {
		t.Fatalf("too far before: %v", err)
	}
	ev, err := m.Map(10_500, "x") // 10.5 s, 0.5 s before first anchor
	if err != nil {
		t.Fatal(err)
	}
	if ev.MediaNanos != 10_500*NanosPerMilli {
		t.Errorf("extrapolated media = %d", ev.MediaNanos)
	}
}

func TestMapperRejectsDuplicateAnchors(t *testing.T) {
	_, err := NewMapper(DefaultTimebase(), []Anchor{
		{ClientNanos: 1}, {ClientNanos: 1},
	})
	if err == nil {
		t.Fatal("duplicate anchor time must be rejected")
	}
}

func TestMapperSortsAnchors(t *testing.T) {
	m, err := NewMapper(DefaultTimebase(), []Anchor{
		{ClientNanos: 2 * NanosPerSecond, MediaNanos: 2 * NanosPerSecond},
		{ClientNanos: 0, MediaNanos: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := m.Anchors()
	if got[0].ClientNanos != 0 {
		t.Fatalf("anchors not sorted: %+v", got)
	}
}

func TestSharedErrorType(t *testing.T) {
	// The clock layer must return the same *serr.Error type used elsewhere.
	_, err := MillisToNanos(math.MaxInt64)
	var target *serr.Error
	if !errors.As(err, &target) {
		t.Fatal("clock error is not *serr.Error")
	}
	if target.Code != serr.CodeClockOverflow {
		t.Fatalf("code=%s", target.Code)
	}
}
