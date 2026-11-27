package quality

import (
	"testing"

	"mediad/internal/model"
	"mediad/internal/timeline"
)

func TestBitrateAndGate(t *testing.T) {
	// 1000 bytes over 100 ms -> 10000 bytes/s -> 80000 bps.
	bps, err := Bitrate(1000, 100*model.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if bps != 80_000 {
		t.Fatalf("bitrate = %d", bps)
	}
	if !WithinBitrateGate(80_000, 83_999) { // +4.99875%
		t.Fatal("just inside 5% must pass")
	}
	if WithinBitrateGate(80_000, 84_001) { // +5.00125%
		t.Fatal("just outside 5% must fail")
	}
	d := DeviationPercent(80_000, 84_000)
	if d < 4.9 || d > 5.1 {
		t.Fatalf("deviation = %v", d)
	}
}

func TestBitrateRejectsBadDuration(t *testing.T) {
	if _, err := Bitrate(10, 0); model.AsError(err).Code != model.ErrUsage {
		t.Fatalf("got %v", err)
	}
}

func TestObjectiveScorePenalizesGaps(t *testing.T) {
	clean := ObjectiveScore(10, 10, nil)
	if clean != 1.0 {
		t.Fatalf("perfect retention = %v", clean)
	}
	withGap := ObjectiveScore(9, 10, []timeline.Gap{{FromSeq: 4, ToSeq: 4}})
	if withGap >= clean {
		t.Fatalf("gap must reduce score: %v", withGap)
	}
	if withGap <= 0 || withGap > 1 {
		t.Fatalf("score out of range: %v", withGap)
	}
	// Deterministic across repeated calls.
	if ObjectiveScore(9, 10, []timeline.Gap{{FromSeq: 4, ToSeq: 4}}) != withGap {
		t.Fatal("score must be deterministic")
	}
}

func TestAlignmentError(t *testing.T) {
	// Perfectly packed timeline: frame starts at cumulative durations -> 0.
	tl := &timeline.Timeline{
		Frames: []timeline.Frame{
			{StartNS: 0, DurationNS: 20 * model.Millisecond},
			{StartNS: 20 * model.Millisecond, DurationNS: 20 * model.Millisecond},
			{StartNS: 40 * model.Millisecond, DurationNS: 20 * model.Millisecond},
		},
	}
	errNS, err := AlignmentError([]*timeline.Timeline{tl})
	if err != nil || errNS != 0 {
		t.Fatalf("clean alignment = %d, %v", errNS, err)
	}
	// Inject a 2 ms hole before the third frame.
	tl.Frames[2].StartNS = 42 * model.Millisecond
	errNS, _ = AlignmentError([]*timeline.Timeline{tl})
	if errNS != int64(2*model.Millisecond) {
		t.Fatalf("alignment error = %d, want 2ms", errNS)
	}
}

func TestMeasureAssemblesReport(t *testing.T) {
	tl := &timeline.Timeline{Frames: []timeline.Frame{
		{StartNS: 0, DurationNS: 100 * model.Millisecond},
	}}
	rep, err := Measure(80_000, 1000, 100*model.Millisecond, 10, 10, nil,
		[]*timeline.Timeline{tl})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Algorithm != MetricAlgorithm || rep.ActualBitrateBPS != 80_000 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.QualityMetric != 1.0 || rep.AlignmentErrorNS != 0 {
		t.Fatalf("metrics wrong: %+v", rep)
	}
}
