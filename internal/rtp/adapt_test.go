package rtp

import (
	"testing"

	"archive/internal/clock"
	"archive/internal/fixture"
)

func testLayers() []Layer {
	return []Layer{
		{Name: "low", TargetKbps: 400, FrameBytes: 5000, FrameIntervalNS: 40000000, TSStep: 1000, PacketsPerFrame: 1},
		{Name: "mid", TargetKbps: 900, FrameBytes: 11250, FrameIntervalNS: 40000000, TSStep: 1000, PacketsPerFrame: 2},
		{Name: "high", TargetKbps: 1400, FrameBytes: 17500, FrameIntervalNS: 40000000, TSStep: 1000, PacketsPerFrame: 3},
	}
}

func TestChooseHighestFittingLayer(t *testing.T) {
	cases := []struct {
		bw   int
		want string
	}{
		{1500, "high"},
		{1100, "mid"},
		{900, "mid"},
		{420, "low"},
		{100, "low"}, // nothing fits; lowest offered
	}
	for _, c := range cases {
		got := ChooseLayers(testLayers(), c.bw)
		if len(got) != 1 || got[0].Name != c.want {
			t.Fatalf("bw %d chose %v, want %s", c.bw, got, c.want)
		}
	}
}

func TestScheduleFollowsBandwidthSeries(t *testing.T) {
	s := Scheduler{Samples: []int{900, 420, 1100}}
	plan, err := s.Plan(testLayers())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mid", "low", "mid"}
	for i := range want {
		if plan[i] != want[i] {
			t.Fatalf("sample %d got %s want %s", i, plan[i], want[i])
		}
	}
}

func TestSchedulerRejectsBadInput(t *testing.T) {
	if _, err := (Scheduler{}).Plan(testLayers()); clock.AsKind(err) != clock.KindInvalidMetadata {
		t.Fatalf("empty series kind = %q", clock.AsKind(err))
	}
	if _, err := (Scheduler{Samples: []int{0}}).Plan(testLayers()); clock.AsKind(err) != clock.KindOutOfRange {
		t.Fatalf("zero bw kind = %q", clock.AsKind(err))
	}
}

func TestActualPlanZeroLossStaysWithinTolerance(t *testing.T) {
	sess, err := fixture.BuildRTP(fixture.RTPPlan{
		PacketCount: 25, PayloadLen: 180, StartSeq: 1, TSStep: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkts, _ := Decode(sess.Wire)
	rec, err := Reorder(pkts)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ActualLayerPlan(testLayers()[0], rec, 1000000000)
	if err != nil {
		t.Fatal(err)
	}
	if plan.FrameCount != 25 {
		t.Fatalf("frames = %d", plan.FrameCount)
	}
	// 25 packets x 180 B over 1 s = 36 kbps payload; measured figure reports it.
	if plan.MeasuredKbps < 35 || plan.MeasuredKbps > 37 {
		t.Fatalf("measured kbps = %.2f", plan.MeasuredKbps)
	}
	if plan.BytesPerFrame() == 0 {
		t.Fatal("bytes per frame not derived")
	}
}

func TestActualPlanLossRaisesDeviation(t *testing.T) {
	sess, _ := fixture.BuildRTP(fixture.RTPPlan{
		PacketCount: 25, PayloadLen: 100, StartSeq: 1, TSStep: 1000,
		LossStart: 10, LossBurst: 5,
	})
	pkts, _ := Decode(sess.Wire)
	rec, _ := Reorder(pkts)
	plan, err := ActualLayerPlan(testLayers()[1], rec, 1000000000)
	if err != nil {
		t.Fatal(err)
	}
	want := 20.0 // 5 of 25 expected packets missing
	if plan.DeviationPct < want-0.01 || plan.DeviationPct > want+0.01 {
		t.Fatalf("deviation = %.3f, want %.1f", plan.DeviationPct, want)
	}
}
