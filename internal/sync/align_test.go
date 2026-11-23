package sync

import (
	"testing"

	"mediaqc/internal/clock"
	"mediaqc/internal/model"
)

func TestDriftWithinToleranceCorrected(t *testing.T) {
	a, err := NewAligner("mat-sync", 20)
	if err != nil {
		t.Fatal(err)
	}
	videoTB, _ := clock.New(90000)
	audioTB, _ := clock.New(48000)
	// video starts at 0; audio starts 10 ms late (within 20 ms tolerance)
	a.Anchor("video", videoTB.At(0))
	a.Anchor("audio", audioTB.At(480)) // 480/48000 = 10 ms

	rep, err := a.Align()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rejected {
		t.Fatal("10 ms drift with 20 ms tolerance must be correctable")
	}
	if len(rep.Corrections) != 2 {
		t.Fatalf("corrections = %d, want 2", len(rep.Corrections))
	}
	var audio *Correction
	for i := range rep.Corrections {
		if rep.Corrections[i].TrackID == "audio" {
			audio = &rep.Corrections[i]
		}
	}
	if audio == nil || audio.Action != "shift" || audio.OffsetNanos != 10_000_000 {
		t.Fatalf("audio correction wrong: %+v", audio)
	}
	if rep.MaxAbsDriftMs < 9.99 || rep.MaxAbsDriftMs > 10.01 {
		t.Fatalf("max drift = %f, want 10ms", rep.MaxAbsDriftMs)
	}
}

func TestDriftBeyondToleranceRejected(t *testing.T) {
	a, _ := NewAligner("mat-sync2", 20)
	videoTB, _ := clock.New(90000)
	audioTB, _ := clock.New(48000)
	a.Anchor("video", videoTB.At(0))
	a.Anchor("audio", audioTB.At(4800)) // 100 ms late > 20 ms

	rep, err := a.Align()
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Rejected {
		t.Fatal("100 ms drift with 20 ms tolerance must be rejected")
	}
	for _, c := range rep.Corrections {
		if c.TrackID == "audio" && (c.Action != "rejected" || c.WithinTolerance) {
			t.Fatalf("audio must be rejected: %+v", c)
		}
	}
}

func TestApplyOffsetKeepsOrdering(t *testing.T) {
	tb, _ := clock.New(90000)
	ev := model.Event{TrackID: "video", Timebase: tb, Start: tb.At(900), PTS: tb.At(900)}
	shifted := ApplyOffset(ev, -10_000_000) // shift 10 ms earlier
	// 10 ms = 900 ticks at 90 kHz
	if shifted.Start.Ticks() != 0 {
		t.Fatalf("shifted start = %d, want 0", shifted.Start.Ticks())
	}
}

func TestCheckMonotonic(t *testing.T) {
	tb, _ := clock.New(1000)
	good := []model.Event{
		{Sequence: 0, Start: tb.At(0)},
		{Sequence: 1, Start: tb.At(100)},
		{Sequence: 2, Start: tb.At(100)}, // equal is allowed
	}
	if err := CheckMonotonic("m", "t", good); err != nil {
		t.Fatalf("equal/ascending timestamps must pass: %v", err)
	}
	bad := []model.Event{
		{Sequence: 0, Start: tb.At(100)},
		{Sequence: 1, Start: tb.At(50)},
	}
	err := CheckMonotonic("m", "t", bad)
	if err == nil || !model.HasCode(err, model.CodeTimestampJump) {
		t.Fatalf("want timestamp-jump error, got %v", err)
	}
}

func TestAlignerValidation(t *testing.T) {
	if _, err := NewAligner("m", -1); err == nil {
		t.Fatal("negative tolerance must be rejected")
	}
	a, _ := NewAligner("m", 10)
	if _, err := a.Align(); err == nil {
		t.Fatal("aligning without anchors must fail")
	}
}
