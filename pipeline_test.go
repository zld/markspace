package mediakernel

import (
	"context"
	"testing"
	"time"
)

func newCleanFixtureData(t *testing.T) []byte {
	t.Helper()
	fx, err := GenerateFixture(FixtureSpec{Seed: 7, PacketCount: 24})
	if err != nil {
		t.Fatal(err)
	}
	return fx.Data
}

func TestPipelineProcessCleanStream(t *testing.T) {
	data := newCleanFixtureData(t)
	p := NewPipeline(RunConfig{QueueCap: 8})
	report, err := p.Process(context.Background(), data)
	if err != nil {
		t.Fatalf("clean stream must not error: %v", err)
	}
	if report.Counters.Packets == 0 {
		t.Fatal("packet count must be positive")
	}
	if report.Counters.LostPackets == 0 || report.Counters.Duplicates == 0 {
		t.Fatalf("fixture must produce losses and duplicates: %+v", report.Counters)
	}
	if report.Counters.QueuePeak > report.Counters.QueueCap || report.Counters.QueueCap != 8 {
		t.Fatalf("queue peak %d must not exceed capacity %d",
			report.Counters.QueuePeak, report.Counters.QueueCap)
	}
	if report.ContentDigest.Hex == "" || report.ContainerDigest.Hex == "" {
		t.Fatal("both digests must be populated")
	}
	if report.Timeline.MissingSamples <= 0 || report.Timeline.DurationTicks <= report.Timeline.MissingSamples {
		t.Fatalf("timeline must reserve gaps and map unique content: %+v", report.Timeline)
	}
	if report.Quality.Applicable != true || report.Quality.SNRProxyDB == nil {
		t.Fatalf("processed stream must carry an SNR proxy: %+v", report.Quality)
	}
	t.Logf("actual bitrate=%.1f bps duration_ms=%.2f", report.Bitrate.ActualBps, report.Timeline.DurationMs)
}

func TestPipelineBackpressurePeakBounded(t *testing.T) {
	data := newCleanFixtureData(t)
	// Tiny queue plus an artificially slow consumer: peak must still be <= cap.
	p := NewPipeline(RunConfig{
		QueueCap:          3,
		SlowConsumerDelay: 2 * time.Millisecond,
	})
	report, err := p.Process(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counters.QueuePeak > 3 {
		t.Fatalf("backpressure violated: peak=%d cap=3", report.Counters.QueuePeak)
	}
	if report.Counters.Pending != 0 || report.Counters.Processed != report.Counters.Packets {
		t.Fatalf("all packets must drain: %+v", report.Counters)
	}
}

func TestPipelineCancellationReportsCounts(t *testing.T) {
	data := newCleanFixtureData(t)
	p := NewPipeline(RunConfig{QueueCap: 4, CancelAfter: 5})
	report, err := p.Process(context.Background(), data)
	if err == nil {
		t.Fatal("cancelled run must return an error")
	}
	if me, ok := AsMediaError(err); !ok || me.Phase != PhaseCancelled {
		t.Fatalf("want cancellation error, got %v", err)
	}
	if !report.Counters.Cancelled {
		t.Fatal("report must flag cancellation")
	}
	if report.Counters.Processed <= 0 || report.Counters.Pending < 0 {
		t.Fatalf("cancelled report must quantify processed/pending: %+v", report.Counters)
	}
}

func TestPipelineCorruptStreamErrorShape(t *testing.T) {
	fx, err := GenerateFixture(FixtureSpec{Seed: 7, Corrupt: CorruptTruncated})
	if err != nil {
		t.Fatal(err)
	}
	cut := TruncateFrame(fx.Data)
	p := NewPipeline(RunConfig{QueueCap: 8})
	report, err := p.Process(context.Background(), cut)
	if err == nil {
		t.Fatal("truncated stream must fail")
	}
	me, ok := AsMediaError(err)
	if !ok {
		t.Fatalf("want structured error, got %v", err)
	}
	if me.Phase != PhaseParse || me.Offset < 0 || me.Reason == "" {
		t.Fatalf("error must carry phase, offset and reason: %+v", me)
	}
	if report == nil || report.Counters.Packets < 0 {
		t.Fatal("a partial quantified report must still be returned")
	}
}

func TestPipelineSpecsAlignmentAndPlan(t *testing.T) {
	data := newCleanFixtureData(t)
	p := NewPipeline(RunConfig{QueueCap: 8, AlignmentTolMs: 25,
		TargetBitrateBps: 1, BitrateTolerance: 1e9}) // target forces a passing deviation
	report, err := p.Process(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	states := []SourceState{{
		SourceID: "src-a", ContentDigest: report.ContentDigest, ContainerDigest: report.ContainerDigest,
	}}
	assets := []AssetDependency{
		{AssetID: "dialogue-a", Kind: "dialogue", Sources: []string{"src-a"}, NeedsContainer: true},
	}
	audio := TimeRange{StartTicks: 0, EndTicks: 48000}
	subs := []SubtitleSpec{
		{ID: "good", Audio: audio, Subtitle: TimeRange{StartTicks: 48, EndTicks: 48048}},   // 1ms
		{ID: "bad", Audio: audio, Subtitle: TimeRange{StartTicks: -4800, EndTicks: 43200}}, // 100ms
	}
	if err := p.ApplySpecs(report, states, assets, subs, nil); err != nil {
		t.Fatal(err)
	}
	if report.IndexDigest.Hex == "" {
		t.Fatal("index digest must be set")
	}
	if report.Plan.RecomputeCount != 1 || report.Plan.ReuseCount != 0 {
		t.Fatalf("first run recomputes the single asset: %+v", report.Plan)
	}
	if report.Alignment.Checked != 2 || report.Alignment.Failed != 1 {
		t.Fatalf("alignment summary wrong: %+v", report.Alignment)
	}
	if report.SpecPass {
		t.Fatal("one subtitle exceeds tolerance: spec must fail")
	}
	if len(report.Failures) == 0 {
		t.Fatal("failures must be enumerated")
	}
	// Second run with identical prior states reuses the asset and passes.
	report2, _ := NewPipeline(RunConfig{QueueCap: 8, AlignmentTolMs: 25,
		TargetBitrateBps: 1, BitrateTolerance: 1e9}).Process(context.Background(), data)
	if err := p.ApplySpecs(report2, states, assets, subs[:1], states); err != nil {
		t.Fatal(err)
	}
	if report2.Plan.ReuseCount != 1 || report2.SpecPass != true {
		t.Fatalf("unchanged rerun must reuse and pass: %+v pass=%v", report2.Plan, report2.SpecPass)
	}
}
