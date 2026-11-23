package runner

import (
	"math"
	"testing"
)

func TestSelfTestEvidenceMeetsSpec(t *testing.T) {
	rep, code := Run(Options{Seed: 17, Window: 8, ToleranceMs: 20})

	// Exit 1: unrecoverable materials exist (double loss/truncation) but the
	// healthy and recovered paths still complete.
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (unrecoverable material, others complete)", code)
	}
	if math.Abs(rep.AVSyncErrorMs) > 20 {
		t.Fatalf("av_sync_error_ms = %f exceeds tolerance 20", rep.AVSyncErrorMs)
	}
	if rep.SingleLossRate != 1.0 {
		t.Fatalf("single loss recovery = %f, want 1.000", rep.SingleLossRate)
	}
	if rep.DoubleLossRate != 0.0 {
		t.Fatalf("double loss recovery = %f, want 0.000", rep.DoubleLossRate)
	}
	if rep.PacketLossRecovery != 1.0 {
		t.Fatalf("headline recovery rate = %f, want 1.000", rep.PacketLossRecovery)
	}
	if !(rep.P95LatencyMs > 0) || math.IsNaN(rep.P95LatencyMs) {
		t.Fatalf("p95 must be finite and positive, got %f", rep.P95LatencyMs)
	}
	if rep.UnrecoverableGaps <= 0 {
		t.Fatal("double-loss/truncation must leave unrecovered gaps")
	}
	if rep.ManifestSHA256 == "" || rep.CheckpointDigest == "" {
		t.Fatal("manifest and checkpoint hashes must be present")
	}
	if rep.InvariantFailures != 0 {
		t.Fatalf("invariants must hold, got %d failures", rep.InvariantFailures)
	}
	if !rep.DeterminismOK {
		t.Fatal("internal double-run determinism check must pass")
	}
}

func TestDeterminismAcrossIndependentRuns(t *testing.T) {
	a, _ := Run(Options{Seed: 17, Window: 8, ToleranceMs: 20})
	b, _ := Run(Options{Seed: 17, Window: 8, ToleranceMs: 20})
	if a.ManifestSHA256 != b.ManifestSHA256 {
		t.Fatal("manifest hash must be identical across runs")
	}
	if a.CheckpointDigest != b.CheckpointDigest {
		t.Fatal("checkpoint digest must be identical across runs")
	}
	if a.P95LatencyMs != b.P95LatencyMs || a.AVSyncErrorMs != b.AVSyncErrorMs {
		t.Fatal("metric values must be identical across runs")
	}
}

func TestThreeDemonstrationPathsPresent(t *testing.T) {
	rep, _ := Run(Options{Seed: 17, Window: 8, ToleranceMs: 20})
	statusByName := map[string]string{}
	for _, p := range rep.Paths {
		statusByName[p.Name] = p.Status
	}
	// Success path.
	if statusByName["healthy"] != "ok" {
		t.Fatalf("healthy path must succeed, got %q", statusByName["healthy"])
	}
	// Recovery path (single loss repaired via FEC -> degraded but no failure).
	if statusByName["single_loss"] == "failed" {
		t.Fatal("single-loss path must recover, not fail")
	}
	// Damage path (double loss unrecoverable).
	if statusByName["double_loss"] == "ok" {
		t.Fatal("double-loss path must not be clean")
	}
	if statusByName["truncated_mp4"] != "failed" {
		t.Fatalf("truncated MP4 must fail, got %q", statusByName["truncated_mp4"])
	}
	if statusByName["bad_timescale"] != "failed" {
		t.Fatalf("zero timescale must fail, got %q", statusByName["bad_timescale"])
	}
}

func TestSeedChangesButRemainsDeterministic(t *testing.T) {
	r1, _ := Run(Options{Seed: 1, Window: 8, ToleranceMs: 20})
	r1b, _ := Run(Options{Seed: 1, Window: 8, ToleranceMs: 20})
	r2, _ := Run(Options{Seed: 99, Window: 8, ToleranceMs: 20})
	if r1.ManifestSHA256 != r1b.ManifestSHA256 {
		t.Fatal("same seed must be deterministic")
	}
	// Different seed should change fixture payloads and thus the manifest hash.
	if r1.ManifestSHA256 == r2.ManifestSHA256 {
		t.Fatal("different seeds should produce different manifest hashes")
	}
}
