package clock

import (
	"math"
	"testing"
)

func TestMapExplicitScaleAndOffset(t *testing.T) {
	m, err := NewMapping(1_000_000, 1, 500)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Map(42)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(42_000_500); got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestMapRejectsZeroScale(t *testing.T) {
	for _, pair := range [][2]int64{{0, 1}, {1, 0}, {-1, 1}} {
		if _, err := NewMapping(pair[0], pair[1], 0); CodeOf(err) != ErrZeroScale {
			t.Fatalf("pair %v: want ErrZeroScale, got %v", pair, err)
		}
	}
	var zero Mapping
	if _, err := zero.Map(1); CodeOf(err) != ErrZeroScale {
		t.Fatalf("uninitialized mapping: want ErrZeroScale, got %v", err)
	}
}

func TestMapOverflow(t *testing.T) {
	m, err := NewMapping(1_000_000_000, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Map(math.MaxInt64 / 2); CodeOf(err) != ErrOverflow {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
	// Offset overflow: large offset plus positive quotient.
	m2, err := NewMapping(1, 1, math.MaxInt64-10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Map(100); CodeOf(err) != ErrOverflow {
		t.Fatalf("offset overflow: want ErrOverflow, got %v", err)
	}
}

func TestTimelineUnmappedTrack(t *testing.T) {
	tl := NewTimeline()
	if _, err := tl.Map(7, 0); CodeOf(err) != ErrUnmappableTrack {
		t.Fatalf("want ErrUnmappableTrack, got %v", err)
	}
	m, _ := NewMapping(1_000_000, 1, 0)
	if err := tl.Bind(7, m); err != nil {
		t.Fatal(err)
	}
	if err := tl.Bind(7, m); err != nil {
		t.Fatalf("identical rebind must succeed: %v", err)
	}
	other, _ := NewMapping(2_000_000, 1, 0)
	if err := tl.Bind(7, other); CodeOf(err) != ErrUnmappableTrack {
		t.Fatalf("conflicting rebind: want ErrUnmappableTrack, got %v", err)
	}
	ns, err := tl.Map(7, 3)
	if err != nil || ns != 3_000_000 {
		t.Fatalf("got %d, %v", ns, err)
	}
}

func TestRTPMapping(t *testing.T) {
	m, err := RTPMapping(90000, 0)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := m.Map(90000) // exactly one second
	if err != nil || ns != 1_000_000_000 {
		t.Fatalf("got %d, %v", ns, err)
	}
	if _, err := RTPMapping(0, 0); CodeOf(err) != ErrZeroScale {
		t.Fatalf("want ErrZeroScale, got %v", err)
	}
}

func TestMatroskaMapping(t *testing.T) {
	m, err := MatroskaMapping(1_000_000, 1, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := m.Map(1000) // 1000 ticks * 1ms = 1s
	if err != nil || ns != 1_000_000_000 {
		t.Fatalf("got %d, %v", ns, err)
	}
	if _, err := MatroskaMapping(0, 1, 1, 0); CodeOf(err) != ErrZeroScale {
		t.Fatalf("want ErrZeroScale, got %v", err)
	}
}
