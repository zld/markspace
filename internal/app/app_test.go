package app

import (
	"context"
	"testing"

	"mediasync/internal/report"
)

func demoConfig() Config {
	return Config{
		Seed: 41, Tracks: []string{"video", "audio", "subtitle"},
		DriftPPM: 180, WrapAt: 900000, Loss: 0.02,
		Workers: 4, Queue: 16, Clusters: 6, PerCluster: 4,
	}
}

func TestRunHealthyEndToEnd(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(context.Background(), demoConfig(), dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := res.Metrics
	if m.TracksMapped != 3 {
		t.Fatalf("tracks_mapped = %d", m.TracksMapped)
	}
	if m.InputBlocks != m.OutputSamples || m.OutputSamples == 0 {
		t.Fatalf("block/sample mismatch: in=%d out=%d", m.InputBlocks, m.OutputSamples)
	}
	if !m.RerunDigestEqual {
		t.Fatal("rerun digest must be equal for deterministic output")
	}
	if m.PacketRecoveryRate < 0.95 {
		t.Fatalf("recovery rate %.4f below 0.95", m.PacketRecoveryRate)
	}
	if m.SyncErrorMSP95 > 20 {
		t.Fatalf("sync p95 %.3f > 20ms", m.SyncErrorMSP95)
	}
	if m.PipelineLatencyMSP95 > 250 {
		t.Fatalf("latency p95 %.3f > 250ms", m.PipelineLatencyMSP95)
	}
}

// deterministicFields masks wall-clock latency, which is inherently
// nondeterministic; every other metric must be exactly reproducible.
func deterministicFields(m report.Metrics) report.Metrics {
	m.PipelineLatencyMSP95 = 0
	return m
}

func TestRunIsDeterministicAcrossInvocations(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	r1, err := Run(context.Background(), demoConfig(), dir1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Run(context.Background(), demoConfig(), dir2)
	if err != nil {
		t.Fatal(err)
	}
	if deterministicFields(r1.Metrics) != deterministicFields(r2.Metrics) {
		t.Fatalf("metrics drift across runs:\n%+v\n%+v", r1.Metrics, r2.Metrics)
	}
}

func TestDamageSuiteClassifiesAllFaults(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(context.Background(), demoConfig(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Verdicts) != res.Metrics.DamagedCases {
		t.Fatalf("verdict count %d != damaged %d", len(res.Verdicts), res.Metrics.DamagedCases)
	}
	names := map[string]bool{}
	var recoverable, rejected int
	for _, v := range res.Verdicts {
		if names[v.Name] {
			t.Fatalf("duplicate verdict %q", v.Name)
		}
		names[v.Name] = true
		if v.Recoverable {
			recoverable++
		} else {
			rejected++
		}
	}
	if recoverable != res.Metrics.RecoverableCases || rejected != res.Metrics.RejectedWrites {
		t.Fatalf("verdict accounting mismatch: %+v", res.Metrics)
	}
	// At least three fault classes must cross parse/sync/output stages.
	required := []string{
		"truncated-cluster", "unknown-length-segment", "timestamp-wrap",
		"drift-mutation", "rtp-windowed-loss", "cancellation-race",
	}
	for _, name := range required {
		if !names[name] {
			t.Fatalf("missing fault class %q", name)
		}
	}
	t.Logf("recoverable=%d rejected=%d verdicts=%v", recoverable, rejected, res.Verdicts)
}

func TestConfigValidation(t *testing.T) {
	bad := demoConfig()
	bad.Workers = 0
	if _, err := Run(context.Background(), bad, t.TempDir()); err == nil {
		t.Fatal("zero workers must fail")
	}
	bad = demoConfig()
	bad.Loss = 0.9
	if _, err := Run(context.Background(), bad, t.TempDir()); err == nil {
		t.Fatal("excessive loss must fail")
	}
	bad = demoConfig()
	bad.Tracks = []string{"hologram"}
	if _, err := Run(context.Background(), bad, t.TempDir()); err == nil {
		t.Fatal("unknown track kind must fail")
	}
}
