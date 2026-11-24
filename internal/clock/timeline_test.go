package clock

import (
	"math"
	"testing"
	"time"
)

func TestExtendWrapForward(t *testing.T) {
	var e Extender
	base := uint32(0xFFFFFFF0)
	first := e.Extend(base)
	for i := uint32(1); i <= 0x30; i++ {
		got := e.Extend(base + i) // wraps past 2^32
		want := first + int64(i)
		if got != want {
			t.Fatalf("step %d: got %d want %d", i, got, want)
		}
	}
}

func TestExtendLatePacketDoesNotRewind(t *testing.T) {
	var e Extender
	e.Extend(0xFFFFFFFE)
	e.Extend(0x00000010) // wrap forward
	late := e.Extend(0xFFFFFFF0)
	if late >= 1<<32 {
		t.Fatalf("late packet should map to pre-wrap range, got %d", late)
	}
	// next in-order packet still extends forward
	if got := e.Extend(0x20); got != 1<<32+0x20 {
		t.Fatalf("post-late extend = %d", got)
	}
}

func TestMediaMsRequiresAnchor(t *testing.T) {
	tl := NewTimeline()
	if _, ok := tl.MediaMs(tl.Extend(1000)); ok {
		t.Fatal("expected uncertain time without anchor")
	}
	if _, ok := tl.WallTime(1000); ok {
		t.Fatal("expected no wall time without anchor")
	}
}

func TestMediaMsWithAnchor(t *testing.T) {
	tl := NewTimeline()
	ntp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tl.AddSenderReport(90000, ntp)
	// 90000 ticks == 1000 ms after anchor
	ext := tl.Extend(180000)
	ms, ok := tl.MediaMs(ext)
	if !ok {
		t.Fatal("expected anchored time")
	}
	if math.Abs(ms-1000) > 0.01 {
		t.Fatalf("media ms = %v, want 1000", ms)
	}
}

func TestMediaMsAcrossWrap(t *testing.T) {
	tl := NewTimeline()
	ntp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tl.AddSenderReport(0xFFFFFF00, ntp)
	ext := tl.Extend(0xFFFFFF00)
	ext2 := tl.Extend(0x00000100) // +0x200 ticks after wrap
	ms, ok := tl.MediaMs(ext2)
	if !ok {
		t.Fatal("expected anchored time")
	}
	want := float64(0x200) / ClockRate * 1000
	if math.Abs(ms-want) > 0.01 {
		t.Fatalf("media ms across wrap = %v, want %v", ms, want)
	}
	_ = ext
}

func TestDriftEstimation(t *testing.T) {
	tl := NewTimeline()
	ntp0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tl.AddSenderReport(0, ntp0)
	// sender clock runs 100 ppm fast: 10 s wall -> 90000*10*(1+100e-6) ticks
	ticks := uint32(90000 * 10 * (1 + 100e-6))
	tl.AddSenderReport(ticks, ntp0.Add(10*time.Second))
	d := tl.DriftPPM()
	if math.Abs(d-100) > 1 {
		t.Fatalf("drift = %v ppm, want ~100", d)
	}
	// wall time compensation: at the second anchor, wall == ntp0+10s
	wt, ok := tl.WallTime(int64(ticks))
	if !ok {
		t.Fatal("expected anchored wall time")
	}
	if d := wt.Sub(ntp0.Add(10 * time.Second)); d < -time.Millisecond || d > time.Millisecond {
		t.Fatalf("wall time off by %v", d)
	}
}
