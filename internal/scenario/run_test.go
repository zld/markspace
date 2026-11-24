package scenario

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/zld/avpipe/internal/fault"
)

func TestNormalScenarioMeetsSyncAndAlignmentGoals(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Seed = 7
	cfg.OutDir = filepath.Join(t.TempDir(), "normal")
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Metrics.ExitCode != 0 {
		t.Fatalf("exit=%d dag=%+v", res.Metrics.ExitCode, res.DAG)
	}
	if res.Metrics.AVSyncMS > 20 {
		t.Fatalf("av_sync_ms=%.3f exceeds 20", res.Metrics.AVSyncMS)
	}
	if res.Metrics.AlignmentErrors != 0 {
		t.Fatalf("alignment errors=%d", res.Metrics.AlignmentErrors)
	}
	if res.Metrics.ManifestSegments != cfg.Segments {
		t.Fatalf("segments=%d want %d", res.Metrics.ManifestSegments, cfg.Segments)
	}
	if len(res.Metrics.ManifestSHA256) != 64 {
		t.Fatalf("manifest sha length=%d", len(res.Metrics.ManifestSHA256))
	}
}

func TestAcceptanceDamageMetricsAndDeterminism(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Seed = 7
	cfg.Loss = 0.05
	cfg.Truncate = 13
	cfg.MissingIndex = true
	cfg.OutDir = filepath.Join(t.TempDir(), "first")

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Metrics.ExitCode != 2 {
		t.Fatalf("exit=%d want 2", res.Metrics.ExitCode)
	}
	if res.CorruptReport == nil || res.CorruptReport.FirstUnreadableOffset != 13 {
		t.Fatalf("corrupt report=%+v want first offset 13", res.CorruptReport)
	}
	if res.Metrics.FirstUnreadable != 13 {
		t.Fatalf("first_unreadable=%d want13", res.Metrics.FirstUnreadable)
	}
	rate := res.Metrics.LossRecoveryRate
	if rate < 0 || rate > 1 {
		t.Fatalf("recovery rate out of range: %v", rate)
	}
	if res.Metrics.RecoveredPackets != res.RTPStats.RecoveredPackets {
		t.Fatal("metric recovered_packets disagrees with RTP stats")
	}
	total := res.RTPStats.RecoveredPackets + res.RTPStats.UnrecoveredGaps
	wantRate := float64(res.RTPStats.RecoveredPackets) / float64(total)
	if math.Abs(rate-wantRate) > 1e-12 {
		t.Fatalf("rate=%v want %v", rate, wantRate)
	}
	if res.Metrics.IndexRebuilt != cfg.Representations*cfg.Segments {
		t.Fatalf("index rebuilt=%d want %d", res.Metrics.IndexRebuilt, cfg.Representations*cfg.Segments)
	}
	if res.Metrics.AlignmentErrors != 0 || res.Metrics.ManifestSegments != cfg.Segments {
		t.Fatalf("alignment=%d segments=%d", res.Metrics.AlignmentErrors, res.Metrics.ManifestSegments)
	}

	cfg2 := cfg
	cfg2.OutDir = filepath.Join(t.TempDir(), "second")
	res2, err := Run(context.Background(), cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if res.ManifestSHA256 != res2.ManifestSHA256 {
		t.Fatalf("same seed changed manifest hash:\n%s\n%s", res.ManifestSHA256, res2.ManifestSHA256)
	}
}

func TestDependencyFailureExitClassThree(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OutDir = filepath.Join(t.TempDir(), "dag")
	cfg.FailNode = "align-representations"
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Metrics.ExitCode != 3 {
		t.Fatalf("exit=%d want3", res.Metrics.ExitCode)
	}
	if res.Metrics.FirstBlockedDependency != "align-representations" {
		t.Fatalf("blocked dependency=%q", res.Metrics.FirstBlockedDependency)
	}
}

func TestInvalidConfigArgumentClass(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Loss = 1.5
	_, err := Run(context.Background(), cfg)
	if code := fault.ExitCode(err); code != 1 {
		t.Fatalf("exit code=%d want1 err=%v", code, err)
	}
}
