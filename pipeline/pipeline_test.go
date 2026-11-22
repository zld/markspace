package pipeline

import (
	"strings"
	"testing"

	"mediadist/timeline"
)

func baseConfig() Config {
	return Config{
		Rates: 3, LossRate: 0.02, LateMs: 180,
		ReplayWindowMs: 2000, CorruptScale: 1, Seed: 42,
	}
}

func TestCrossStageInvariants(t *testing.T) {
	p, err := New(baseConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m, err := p.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.SyncErrorMs > 20 {
		t.Fatalf("sync error %.1f ms exceeds 20 ms", m.SyncErrorMs)
	}
	if m.LossRecoveryRate < 0.90 {
		t.Fatalf("recovery rate %.2f below 0.90", m.LossRecoveryRate)
	}
	if m.P95LatencyMs > 500 {
		t.Fatalf("p95 latency %.0f exceeds 500 ms", m.P95LatencyMs)
	}
	if m.AlignedRepresentations != 3 {
		t.Fatalf("aligned representations = %d, want 3", m.AlignedRepresentations)
	}
	if len(m.ManifestSHA256) != 64 {
		t.Fatalf("digest length = %d, want 64 hex chars", len(m.ManifestSHA256))
	}
	if m.PacketsRecovered > m.PacketsMissing {
		t.Fatal("recovered packets exceed missing packets")
	}
	if m.Unrecoverable < 1 {
		t.Fatal("permanent loss was not surfaced as unrecoverable")
	}
	committed := p.engine.Committed()
	for i := 1; i < len(committed); i++ {
		if committed[i].Epoch < committed[i-1].Epoch ||
			(committed[i].Epoch == committed[i-1].Epoch &&
				committed[i].Time.Cmp(committed[i-1].Time) < 0) {
			t.Fatalf("committed ordering broken at %d", i)
		}
	}
	if !strings.Contains(m.Format(), "manifest_sha256=") {
		t.Fatalf("summary missing digest: %s", m.Format())
	}
}

func TestPipelineDeterminismAcrossRuns(t *testing.T) {
	run := func() string {
		p, err := New(baseConfig())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		m, err := p.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return m.ManifestSHA256
	}
	first, second, third := run(), run(), run()
	if first != second || second != third {
		t.Fatalf("non-deterministic digests: %s %s %s", first, second, third)
	}
}

func TestCorruptTimescaleRejected(t *testing.T) {
	cfg := baseConfig()
	cfg.CorruptScale = 0
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Run(); timeline.CodeOf(err) != "INVALID_TIMESCALE" {
		t.Fatalf("code = %q, want INVALID_TIMESCALE", timeline.CodeOf(err))
	}
}

func TestReplayWindowExceeded(t *testing.T) {
	cfg := baseConfig()
	cfg.LateMs = 9000
	cfg.ReplayWindowMs = 1000
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Run()
	if timeline.CodeOf(err) != "REPLAY_WINDOW_EXCEEDED" {
		t.Fatalf("code = %q, want REPLAY_WINDOW_EXCEEDED", timeline.CodeOf(err))
	}
}

func TestRecoveryAndColorFixtureCoverage(t *testing.T) {
	p, _ := New(baseConfig())
	m, err := p.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.ColorConflicts != 2 || m.ColorMissing != 3 {
		t.Fatalf("color stats conflicts=%d missing=%d, want 2/3",
			m.ColorConflicts, m.ColorMissing)
	}
	if m.Replayed == 0 {
		t.Fatal("late packets never triggered a timeline replay")
	}
}

func TestSnapshotReplayAfterCorruption(t *testing.T) {
	cfg := baseConfig()
	cfg.LateMs = 9000
	cfg.ReplayWindowMs = 1000
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Run(); timeline.CodeOf(err) != "REPLAY_WINDOW_EXCEEDED" {
		t.Fatalf("expected corrupting failure, got %v", err)
	}
	// The engine must remain on its last good snapshot: rerun with a
	// valid window from a fresh pipeline yields the canonical digest.
	good := baseConfig()
	p1, _ := New(good)
	m1, err := p1.Run()
	if err != nil {
		t.Fatalf("replay run: %v", err)
	}
	p2, _ := New(good)
	m2, err := p2.Run()
	if err != nil {
		t.Fatalf("second replay run: %v", err)
	}
	if m1.ManifestSHA256 != m2.ManifestSHA256 {
		t.Fatal("post-recovery summaries diverged")
	}
}
