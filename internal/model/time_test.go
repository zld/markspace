package model

import "testing"

func TestTimescaleRoundTrip(t *testing.T) {
	tb := Timescale(90000)
	m, err := tb.ToMedia(90000)
	if err != nil {
		t.Fatal(err)
	}
	if m != MediaTime(1_000_000_000) {
		t.Fatalf("1 second at 90kHz = %d ns, want 1e9", m)
	}
	back, err := tb.FromMedia(m)
	if err != nil {
		t.Fatal(err)
	}
	if back != 90000 {
		t.Fatalf("round trip = %d, want 90000", back)
	}
}

func TestInvalidTimebase(t *testing.T) {
	if _, err := (Timebase{}).ToMedia(1); err != ErrInvalidTimebase {
		t.Fatalf("got %v, want ErrInvalidTimebase", err)
	}
}

func TestOverflowDetected(t *testing.T) {
	tb := Timescale(90000)
	if _, err := tb.ToMedia(1 << 60); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestTickDuration(t *testing.T) {
	d, err := Timescale(48000).TickDuration()
	if err != nil {
		t.Fatal(err)
	}
	if d <= 0 {
		t.Fatalf("tick duration = %d, want > 0", d)
	}
}
