package quality

import (
	"testing"

	"pwf/internal/ingest"
	"pwf/internal/media"
)

func rtpResult(track string, records, gaps, dup, ooo int) *ingest.RTPStreamResult {
	tb, _ := media.NewTimeBase(48000)
	res := &ingest.RTPStreamResult{Track: track, Codec: ingest.CodecOpus, ClockHz: 48000,
		DupCount: dup, OutOfOrder: ooo}
	for i := 0; i < records; i++ {
		d, _ := media.NewDuration(960, tb)
		res.Records = append(res.Records, ingest.RTPRecord{
			ExtSequence: uint64(i), Duration: d, Payload: make([]byte, 40),
		})
	}
	for g := 0; g < gaps; g++ {
		res.Gaps = append(res.Gaps, ingest.GapRecord{Track: track, StartSeq: uint64(100 + g*10), Count: 2})
	}
	return res
}

func TestMeasureRTPCountsAndBitrate(t *testing.T) {
	res := rtpResult("a", 10, 1, 1, 0)
	m, err := MeasureRTP(res)
	if err != nil {
		t.Fatal(err)
	}
	if m.PacketCount != 10 || m.MissingPackets != 2 || m.DupCount != 1 {
		t.Fatalf("counts wrong: %+v", m)
	}
	if m.MaxGap != 2 || m.FirstGapSeq != 100 {
		t.Fatalf("gap stats wrong: %+v", m)
	}
	// 10 packets * 40 bytes over 10*20ms = 400 bytes / 0.2s = 16000 bps.
	if m.AvgBitrateBps != 16000 {
		t.Fatalf("bitrate got %d want 16000", m.AvgBitrateBps)
	}
	if m.TotalTicks != 10*960 {
		t.Fatalf("ticks got %d", m.TotalTicks)
	}
	// anomalies = 2 missing + 1 dup = 3 over expected 12.
	if m.AnomalyRatio.Num != 3 || m.AnomalyRatio.Den != 12 {
		t.Fatalf("anomaly ratio got %s", m.AnomalyRatio)
	}
}

func TestMeasureRTPRejectsEmpty(t *testing.T) {
	if _, err := MeasureRTP(&ingest.RTPStreamResult{Track: "x"}); err == nil {
		t.Fatal("empty stream must fail measurement")
	}
}

func TestRationalComparison(t *testing.T) {
	low := Rational{Num: 1, Den: 100}  // 1%
	high := Rational{Num: 5, Den: 100} // 5%
	if !low.LessThan(high) {
		t.Fatal("1%% should be below 5%%")
	}
	if high.LessThan(low) {
		t.Fatal("5%% cannot be below 1%%")
	}
}

func TestPlanPassthroughWhenAllGatesPass(t *testing.T) {
	m, _ := MeasureRTP(rtpResult("clean", 10, 0, 0, 0))
	plan, err := PlanTracks([]*Measurement{m}, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tracks[0].Decision != DecisionPassthrough {
		t.Fatalf("expected passthrough, got %s", plan.Tracks[0].Decision)
	}
}

func TestPlanRejectOnMaxGap(t *testing.T) {
	res := rtpResult("gappy", 20, 0, 0, 0)
	res.Gaps = []ingest.GapRecord{{Track: "gappy", StartSeq: 5, Count: 5}}
	m, _ := MeasureRTP(res)
	plan, err := PlanTracks([]*Measurement{m}, DefaultThresholds())
	if err != nil {
		t.Fatalf("rejection is a plan result, not an error: %v", err)
	}
	if plan.Tracks[0].Decision != DecisionReject {
		t.Fatalf("expected reject, got %s", plan.Tracks[0].Decision)
	}
	if len(plan.Tracks[0].UnmetConstraints) == 0 {
		t.Fatal("reject must name the unmet constraint")
	}
}

func TestPlanTranscodeOnBitrateAndBudgetFailure(t *testing.T) {
	// High bitrate forces transcode under tight budget => infeasible error.
	res := &ingest.RTPStreamResult{Track: "heavy", Codec: ingest.CodecAAC, ClockHz: 44100}
	tb, _ := media.NewTimeBase(44100)
	for i := 0; i < 12; i++ {
		d, _ := media.NewDuration(1024, tb)
		res.Records = append(res.Records, ingest.RTPRecord{
			Duration: d, Payload: make([]byte, 2000),
		})
	}
	m, err := MeasureRTP(res)
	if err != nil {
		t.Fatal(err)
	}
	if m.AvgBitrateBps <= DefaultThresholds().TargetBitrateBps {
		t.Fatalf("test fixture should exceed bitrate target, got %d", m.AvgBitrateBps)
	}
	plan, perr := PlanTracks([]*Measurement{m}, TightBudget())
	if perr == nil || !plan.Infeasible {
		t.Fatal("tight budget with required transcode must fail planning")
	}
	me, ok := media.AsError(perr)
	if !ok || me.Kind != media.KindPlanInfeasible {
		t.Fatalf("expected plan-infeasible media error, got %v", perr)
	}
}

func TestPlanTranscodeFitsNormalBudget(t *testing.T) {
	res := &ingest.RTPStreamResult{Track: "heavy", Codec: ingest.CodecAAC, ClockHz: 44100}
	tb, _ := media.NewTimeBase(44100)
	for i := 0; i < 12; i++ {
		d, _ := media.NewDuration(1024, tb)
		res.Records = append(res.Records, ingest.RTPRecord{Duration: d, Payload: make([]byte, 2000)})
	}
	m, _ := MeasureRTP(res)
	plan, err := PlanTracks([]*Measurement{m}, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tracks[0].Decision != DecisionTranscode {
		t.Fatalf("expected transcode, got %s", plan.Tracks[0].Decision)
	}
	if plan.TotalCostCents != DefaultThresholds().TranscodeCostCents {
		t.Fatalf("cost accounting wrong: %d", plan.TotalCostCents)
	}
}
