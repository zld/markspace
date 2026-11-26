package budget

import (
	"testing"
	"time"
)

func TestAdaptTiers(t *testing.T) {
	// 48 kHz stereo: 24-bit 2.304 Mbps, 16-bit 1.536 Mbps.
	high, err := Adapt(3_000_000, 48000, 2) // 80% = 2.4M
	if err != nil || high.Bits != 24 || high.TargetBPS != 2_304_000 || !high.Delivered {
		t.Fatalf("high=%+v err=%v", high, err)
	}
	mid, err := Adapt(2_000_000, 48000, 2) // budget 1.6M
	if err != nil || mid.Bits != 16 || mid.TargetBPS != 1_536_000 {
		t.Fatalf("mid=%+v err=%v", mid, err)
	}
	low, err := Adapt(1_000_000, 48000, 2) // budget 0.8M
	if err != nil || low.Delivered || low.Bits != 0 {
		t.Fatalf("low=%+v err=%v", low, err)
	}
}

func TestSlidingWindowExpiresOldBandwidth(t *testing.T) {
	w, err := NewWindow(10 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddBPS(0, 9_000_000); err != nil {
		t.Fatal(err)
	}
	if err := w.AddBPS(10*time.Second, 1_000_000); err != nil {
		t.Fatal(err)
	}
	// At t=20s, first sample at 0 is exactly two widths old and excluded.
	got := w.Bandwidth(20 * time.Second)
	if got != 1_000_000 {
		t.Fatalf("bandwidth=%.0f want 1000000 after expiry", got)
	}
}

func TestTargetBPSOverflowGuard(t *testing.T) {
	if _, err := TargetBPS(192000, 8, 24); err != nil {
		t.Fatalf("maximum legal config should fit: %v", err)
	}
	if _, err := TargetBPS(1<<60, 1<<20, 24); err == nil {
		t.Fatal("expected overflow error")
	}
}
