package clock

import (
	"errors"
	"testing"
	"time"

	"replay/internal/model"
)

func TestAdvanceProducesMonotonicRTPTimestamps(t *testing.T) {
	c, err := New(90000, 0x1234)
	if err != nil {
		t.Fatal(err)
	}
	first := c.Advance(90000)  // 1s of 90kHz media
	second := c.Advance(45000) // 0.5s
	if second-first != 90000 {
		t.Fatalf("RTP delta = %d, want 90000", second-first)
	}
	third := c.Advance(0)
	if third-second != 45000 {
		t.Fatalf("RTP delta = %d, want 45000", third-second)
	}
}

func TestZeroRateRejected(t *testing.T) {
	if _, err := New(0, 1); err == nil {
		t.Fatal("expected error for zero clock rate")
	}
	if _, err := ToDuration(100, 0); err == nil {
		t.Fatal("expected error for zero rate in ToDuration")
	}
	if _, err := FromDuration(time.Second, 0); err == nil {
		t.Fatal("expected error for zero rate in FromDuration")
	}
}

func TestToDurationExact(t *testing.T) {
	d, err := ToDuration(90000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if d != time.Second {
		t.Fatalf("got %v, want 1s", d)
	}
	d, err = ToDuration(48000+24000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if d != 1500*time.Millisecond {
		t.Fatalf("got %v, want 1.5s", d)
	}
}

func TestToDurationOverflow(t *testing.T) {
	_, err := ToDuration(1<<62, 1)
	if !errors.Is(err, model.ErrOverflow) {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestFromDurationRoundTrip(t *testing.T) {
	units, err := FromDuration(2500*time.Millisecond, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if units != 120000 {
		t.Fatalf("got %d units, want 120000", units)
	}
	back, err := ToDuration(units, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if back != 2500*time.Millisecond {
		t.Fatalf("round trip mismatch: %v", back)
	}
	if _, err := FromDuration(-time.Second, 48000); !errors.Is(err, model.ErrOverflow) {
		t.Fatalf("negative duration: want ErrOverflow, got %v", err)
	}
}
