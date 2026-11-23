package pipeline

import (
	"strings"

	"mediad/internal/model"
	"testing"
)

func run(t *testing.T, cfg Config) model.Summary {
	t.Helper()
	sum, err := Run(cfg, nil)
	if err != nil {
		t.Fatalf("run %s: %v", cfg.Fixture, err)
	}
	return sum
}

func TestNormalMetricsMeetTargets(t *testing.T) {
	sum := run(t, Config{Seed: 42, Fixture: "normal", Loss: 0.05, ReorderMs: 80, LateWindowMs: 250, CacheSize: 512})
	if sum.AVSyncErrorMs > 40 {
		t.Errorf("av_sync_error_ms = %d, want <= 40", sum.AVSyncErrorMs)
	}
	if sum.RecoveryRate < 0.80 {
		t.Errorf("recovery_rate = %.3f, want >= 0.80", sum.RecoveryRate)
	}
	if sum.P95LatencyMs > 250 {
		t.Errorf("p95_latency_ms = %d, want <= 250", sum.P95LatencyMs)
	}
	if sum.MalformedPackets != 0 {
		t.Errorf("malformed_packets = %d, want 0", sum.MalformedPackets)
	}
	if sum.LostPackets == 0 {
		t.Fatal("normal sample lost nothing; recovery rate is vacuous")
	}
	if sum.RecoveredPackets > sum.LostPackets {
		t.Errorf("recovered %d > lost %d", sum.RecoveredPackets, sum.LostPackets)
	}
	if sum.EmittedSamples == 0 || sum.IndexRebuilt == 0 {
		t.Errorf("empty output: emitted=%d index=%d", sum.EmittedSamples, sum.IndexRebuilt)
	}
}

func TestDeterministicRepeat(t *testing.T) {
	cfg := Config{Seed: 7, Fixture: "normal", Loss: 0.05, ReorderMs: 80, LateWindowMs: 250, CacheSize: 512}
	a := run(t, cfg)
	b := run(t, cfg)
	if a.ResultSHA256 != b.ResultSHA256 {
		t.Fatalf("digest differs between runs: %s vs %s", a.ResultSHA256, b.ResultSHA256)
	}
	if len(a.ResultSHA256) != 64 {
		t.Fatalf("digest is not a sha256 hex string: %q", a.ResultSHA256)
	}
}

func TestDifferentSeedDifferentDigest(t *testing.T) {
	a := run(t, Config{Seed: 1, Fixture: "normal", Loss: 0.05, ReorderMs: 80, LateWindowMs: 250, CacheSize: 512})
	b := run(t, Config{Seed: 2, Fixture: "normal", Loss: 0.05, ReorderMs: 80, LateWindowMs: 250, CacheSize: 512})
	if a.ResultSHA256 == b.ResultSHA256 {
		t.Fatal("different seeds produced the same digest")
	}
}

func TestWrapQuarantinesAmbiguous(t *testing.T) {
	sum := run(t, Config{Seed: 42, Fixture: "wrap", Loss: -1, ReorderMs: -1, LateWindowMs: 250})
	if sum.QuarantinedPackets == 0 {
		t.Fatal("ambiguous rollover packet was not quarantined")
	}
	if sum.EmittedSamples == 0 {
		t.Fatal("wrap sample emitted nothing")
	}
}

func TestTruncationReported(t *testing.T) {
	sum := run(t, Config{Seed: 42, Fixture: "truncated", Loss: -1, ReorderMs: -1, LateWindowMs: 250})
	if sum.TruncatedTail == 0 {
		t.Fatal("truncated tail was not reported")
	}
	if sum.MalformedPackets == 0 {
		t.Fatal("truncated sample reported no malformed packets")
	}
	// Valid packets before the tail still reach the timeline.
	if sum.EmittedSamples == 0 {
		t.Fatal("valid packets were lost along with the truncated tail")
	}
}

func TestCorruptionPropagatesToBufferAndSync(t *testing.T) {
	sum := run(t, Config{Seed: 42, Fixture: "corrupt", Loss: -1, ReorderMs: -1, LateWindowMs: 250})
	if sum.MalformedPackets == 0 {
		t.Fatal("corruption produced no malformed count")
	}
	if sum.BufferMalformed == 0 {
		t.Fatal("corruption did not reach buffer statistics")
	}
	if sum.SyncMalformed == 0 {
		t.Fatal("corruption did not reach sync statistics")
	}
	// The corrupt sample still emits the surrounding good packets.
	if sum.EmittedSamples == 0 {
		t.Fatal("packets after the corrupt one were not emitted")
	}
}

func TestLateFeedbackReplaysReproducibly(t *testing.T) {
	cfg := Config{Seed: 42, Fixture: "late-rtcp", Loss: -1, ReorderMs: -1, LateWindowMs: 250}
	a := run(t, cfg)
	b := run(t, cfg)
	if a.LateReplayed == 0 {
		t.Fatal("late feedback did not trigger a replay")
	}
	if a.LateFeedback == 0 {
		t.Fatal("late feedback was not counted")
	}
	if a.ResultSHA256 != b.ResultSHA256 {
		t.Fatal("late replay is not reproducible")
	}
	// Replay must also shrink the observable sync sample set relative to
	// the pre-replay run; assert the recomputed metrics are present.
	if a.AVSyncSamples == 0 {
		t.Fatal("sync metrics missing after replay")
	}
}

func TestMissingSenderReportKeepsLowConfidence(t *testing.T) {
	sum := run(t, Config{Seed: 42, Fixture: "no-sr", Loss: -1, ReorderMs: -1, LateWindowMs: 250})
	if sum.AVSyncConfidence != "low" {
		t.Fatalf("confidence = %q, want low", sum.AVSyncConfidence)
	}
	if sum.AVSyncErrorMs > 500 {
		t.Fatalf("bounded fallback exceeded its bound: %d", sum.AVSyncErrorMs)
	}
}

func TestClockDriftIsObservable(t *testing.T) {
	sum := run(t, Config{Seed: 42, Fixture: "clock-drift", Loss: -1, ReorderMs: -1, LateWindowMs: 250})
	if sum.ClockDriftPPM <= 0 {
		t.Fatalf("clock drift not detected: %d ppm", sum.ClockDriftPPM)
	}
	if sum.AVSyncConfidence != "high" {
		t.Fatalf("clock-drift sample lost SR confidence: %q", sum.AVSyncConfidence)
	}
}

func TestRecoveryCacheBoundIsCounted(t *testing.T) {
	// A 4-packet cache cannot answer feedback for all losses; the shortfall
	// must surface as cache misses rather than fabricated samples.
	sum := run(t, Config{Seed: 42, Fixture: "normal", Loss: 0.2, ReorderMs: 80, LateWindowMs: 250, CacheSize: 4})
	if sum.CacheMiss == 0 {
		t.Fatal("bounded cache produced no misses")
	}
	if sum.RecoveredPackets > sum.LostPackets {
		t.Fatalf("recovered %d exceeds lost %d", sum.RecoveredPackets, sum.LostPackets)
	}
}

func TestUnknownFixtureAndBadParams(t *testing.T) {
	if _, err := Run(Config{Fixture: "nope"}, nil); err == nil {
		t.Fatal("unknown fixture accepted")
	}
	if _, err := Run(Config{Fixture: "normal", Loss: 2}, nil); err == nil {
		t.Fatal("loss out of range accepted")
	}
}

func TestAllFixturesRun(t *testing.T) {
	for _, name := range FixtureNames() {
		sum := run(t, Config{Seed: 42, Fixture: name, Loss: -1, ReorderMs: -1, LateWindowMs: 250})
		if len(sum.ResultSHA256) != 64 {
			t.Errorf("%s: bad digest %q", name, sum.ResultSHA256)
		}
	}
	if strings.Join(FixtureNames(), ",") != "normal,wrap,truncated,corrupt,late-rtcp,no-sr,clock-drift" {
		t.Fatalf("fixture order changed: %v", FixtureNames())
	}
}
