package planner

import (
	"math"
	"strings"
	"testing"
	"time"

	"replay/internal/events"
	"replay/internal/model"
)

func req(win events.Window, bitrate int64) Request {
	return Request{
		Window:        win,
		TargetBitrate: bitrate,
		AudioIn:       model.AudioInfo{Channels: 2, SampleRate: 48000},
		ColorIn:       nil,
	}
}

func window(s, e time.Duration) events.Window { return events.Window{Start: s, End: e} }

func TestEvaluateAccepts(t *testing.T) {
	rep := &model.Report{}
	c, reasons := Evaluate(req(window(9*time.Second, 17*time.Second), 1_800_000), DefaultLimits(), rep)
	if reasons != nil {
		t.Fatalf("unexpected rejection: %v", reasons)
	}
	if c.Duration != 8*time.Second {
		t.Fatalf("duration = %v", c.Duration)
	}
	if c.VideoBitrate != 1_800_000-AudioBitrate {
		t.Fatalf("video bitrate = %d", c.VideoBitrate)
	}
	wantEst := int64(1_800_000 / 8 * 8)
	if c.EstBytes != wantEst {
		t.Fatalf("est bytes = %d, want %d", c.EstBytes, wantEst)
	}
	if rep.Count(model.CodeBudgetExceeded) != 0 {
		t.Fatal("no budget issue expected")
	}
}

func TestEvaluateRejectsBitrate(t *testing.T) {
	rep := &model.Report{}
	c, reasons := Evaluate(req(window(0, 8*time.Second), 50_000_000), DefaultLimits(), rep)
	if c != nil || len(reasons) == 0 {
		t.Fatalf("expected rejection, got %+v", c)
	}
	if !strings.Contains(reasons[0], "above max") {
		t.Fatalf("unexpected reason: %v", reasons)
	}
	if rep.Count(model.CodeBudgetExceeded) != 1 {
		t.Fatal("expected BUDGET_EXCEEDED issue")
	}
}

func TestEvaluateRejectsStarvedVideo(t *testing.T) {
	rep := &model.Report{}
	// 200k total minus 128k audio leaves 72k video: below minimum.
	_, reasons := Evaluate(req(window(0, 8*time.Second), 200_000), DefaultLimits(), rep)
	found := false
	for _, r := range reasons {
		if strings.Contains(r, "after reserving") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected video starvation reason: %v", reasons)
	}
}

func TestEvaluateRejectsUnsupportedAudio(t *testing.T) {
	rep := &model.Report{}
	r := req(window(0, 8*time.Second), 1_800_000)
	r.AudioIn = model.AudioInfo{Channels: 6, SampleRate: 48000}
	c, reasons := Evaluate(r, DefaultLimits(), rep)
	if c != nil || len(reasons) == 0 {
		t.Fatal("expected rejection for 5.1 layout")
	}
	if rep.Count(model.CodeUnsupportedAudio) != 1 {
		t.Fatal("expected UNSUPPORTED_AUDIO issue")
	}
}

func TestEvaluateRejectsHugeWindow(t *testing.T) {
	rep := &model.Report{}
	_, reasons := Evaluate(req(window(0, time.Hour), 1_800_000), DefaultLimits(), rep)
	found := false
	for _, r := range reasons {
		if strings.Contains(r, "exceeds max") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected window limit reason: %v", reasons)
	}
}

func TestMulDivSatExactAndOverflow(t *testing.T) {
	if got := mulDivSat(225000, int64(8*time.Second), int64(time.Second)); got != 1_800_000 {
		t.Fatalf("normal estimate = %d", got)
	}
	if got := mulDivSat(8000, 1_800_000, 1_000_000); got != 14400 {
		t.Fatalf("cost = %d, want 14400", got)
	}
	// Product would wrap int64/uint64; must saturate rather than go negative.
	if got := mulDivSat(math.MaxInt64, math.MaxInt64, 1); got != math.MaxInt64 {
		t.Fatalf("overflow = %d, want saturation", got)
	}
	if got := mulDivSat(-1, 10, 1); got != 0 {
		t.Fatalf("negative factor = %d, want 0", got)
	}
}

func TestEvaluateExtremeBudgetNeverNegativeEstimate(t *testing.T) {
	rep := &model.Report{}
	// ~100 years window and maximal bitrate: arithmetic must saturate and the
	// size/cost rejection reasons must still be produced (not wrap negative).
	_, reasons := Evaluate(req(window(0, 100*365*24*time.Hour), math.MaxInt64), Limits{
		MaxWindow:    time.Duration(math.MaxInt64),
		MinBitrate:   1,
		MaxBitrate:   math.MaxInt64,
		MaxEstBytes:  1 << 30,
		MaxCostUnits: 1,
	}, rep)
	joined := strings.Join(reasons, "\n")
	if !strings.Contains(joined, "estimated size 9223372036854775807 bytes") {
		t.Fatalf("expected saturated size reason, got: %v", reasons)
	}
	if !strings.Contains(joined, "estimated cost 9223372036854775807 units") {
		t.Fatalf("expected saturated cost reason, got: %v", reasons)
	}
}
