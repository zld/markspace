package slicer

import (
	"testing"
	"time"

	"streamcheck/internal/clock"
)

func TestWindowCutsAtKeyframeOnly(t *testing.T) {
	w := NewWindow(WindowConfig{TargetSegment: 3 * time.Second})
	// 90 kHz ticks; one frame every 3000 ticks (~33 ms).
	w.ObservePTS(0)
	w.OpenSegment(0)
	// Non-keyframes before the target must not cut.
	pts := uint64(270_000) // 3s exactly, but not a keyframe
	if w.ShouldCut(false, pts) {
		t.Fatal("non-keyframe must not trigger a cut")
	}
	// A keyframe slightly past the target cuts and records drift.
	pts = 273_000 // 3.033s
	if !w.ShouldCut(true, pts) {
		t.Fatal("keyframe past target must cut")
	}
	rec := w.CloseSegment("seg0.ts", pts, true)
	if rec.Sequence != 0 || rec.EndPTS != 273_000 {
		t.Fatalf("record: %+v", rec)
	}
	// 273000 ticks = 3.0333s; drift is reported in whole milliseconds.
	if rec.DriftMS != 33 {
		t.Fatalf("drift: %d", rec.DriftMS)
	}
}

func TestWindowDelaysCutUntilKeyframe(t *testing.T) {
	w := NewWindow(WindowConfig{TargetSegment: 2 * time.Second})
	w.ObservePTS(0)
	w.OpenSegment(0)
	// At 2.5s still no keyframe: no cut even though target passed.
	if w.ShouldCut(false, 225_000) {
		t.Fatal("cut before a keyframe")
	}
	// Keyframe arrives at 2.667s; drift is positive.
	if !w.ShouldCut(true, 240_000) {
		t.Fatal("keyframe should cut")
	}
	rec := w.CloseSegment("seg0.ts", 240_000, true)
	// 240000 ticks = 2.66667s; whole-millisecond truncation yields 666ms.
	if rec.DriftMS != 666 {
		t.Fatalf("expected delayed-cut drift 666ms, got %d", rec.DriftMS)
	}
	if w.MaxAbsDriftMS() != 666 {
		t.Fatalf("max drift: %d", w.MaxAbsDriftMS())
	}
}

func TestWindowRollingRetention(t *testing.T) {
	w := NewWindow(WindowConfig{
		TargetSegment: time.Second, WindowSize: 3,
	})
	w.ObservePTS(0)
	for i := 0; i < 5; i++ {
		start := uint64(i) * 90_000
		w.OpenSegment(start)
		w.CloseSegment("", start+90_000, true)
	}
	if got := len(w.Records()); got != 3 {
		t.Fatalf("retained: %d, want 3", got)
	}
	if w.Records()[0].Sequence != 2 {
		t.Fatalf("oldest retained sequence: %d", w.Records()[0].Sequence)
	}
	if w.NextSequence() != 5 {
		t.Fatalf("next sequence must keep growing: %d", w.NextSequence())
	}
}

func TestWindowPTSWrapFeedsClock(t *testing.T) {
	w := NewWindow(WindowConfig{TargetSegment: time.Second})
	// Drive a wrap through the window; the expanded line must grow.
	w.ObservePTS(clock.Mod - 100)
	exp, kind := w.ObservePTS(200)
	if kind != clock.JumpWrap {
		t.Fatalf("expected wrap, got %v", kind)
	}
	if exp != clock.Mod+200 {
		t.Fatalf("expanded across wrap: %d", exp)
	}
}

func TestDefaultTargetDuration(t *testing.T) {
	w := NewWindow(WindowConfig{})
	if w.cfg.TargetSegment != 3*time.Second {
		t.Fatalf("default target: %v", w.cfg.TargetSegment)
	}
}
