package pipeline_test

import (
	"testing"
	"time"

	"mediakey/internal/fixture"
	"mediakey/internal/license"
	"mediakey/internal/manifest"
	"mediakey/internal/pipeline"
)

// TestHealthyRoundTrip covers the full cross-module path: fixture -> manifest
// -> license -> dispatcher -> crypto -> replay cache -> results.
func TestHealthyRoundTrip(t *testing.T) {
	cfg, m := buildHealthy(t, 9, 128, 3)
	results, rotations, err := pipeline.Verify(cfg)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(results) != len(m.Chunks) {
		t.Fatalf("got %d results, want %d", len(results), len(m.Chunks))
	}
	for _, r := range results {
		if r.Status != manifest.StatusOK {
			t.Fatalf("chunk %d unexpectedly failed: %s", r.ChunkIndex, r.FailureReason)
		}
		if r.KeyID == "" {
			t.Fatalf("chunk %d missing key id", r.ChunkIndex)
		}
	}
	// rotate-every 3 over 9 chunks activates rounds 1 and 2 -> 2 rotations.
	if rotations != 2 {
		t.Fatalf("rotation_count = %d, want 2", rotations)
	}
}

// TestOneFailureDoesNotMaskOthers ensures every chunk still reports, and the
// failure categories remain independently attributable.
func TestOneFailureDoesNotMaskOthers(t *testing.T) {
	cfg, _ := buildHealthy(t, 6, 64, 2)
	cfg.Faults = map[int]string{2: fixture.InjectTamper}
	results, _, err := pipeline.Verify(cfg)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(results) != 6 {
		t.Fatalf("expected all 6 chunk results, got %d", len(results))
	}
	failed := 0
	for _, r := range results {
		if r.Status == manifest.StatusFailed {
			failed++
			if r.ChunkIndex != 2 || r.FailureReason != manifest.ReasonAuthFailed {
				t.Fatalf("unexpected failure: %+v", r)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("want exactly 1 failed chunk, got %d", failed)
	}
}

// TestReplayAndExpiredChains exercises the replay and license error links.
func TestReplayAndExpiredChains(t *testing.T) {
	cfg, _ := buildHealthy(t, 6, 64, 2)
	// Expired license with no grace: every strict check reports expired,
	// while the replayed nonce is rejected first at the replay layer.
	cfg.Lic.NotAfter = cfg.Now.Unix() - 1
	cfg.Lic.GraceWindow = 0
	if err := cfg.Lic.Sign(cfg.SigningKey); err != nil {
		t.Fatalf("resign: %v", err)
	}
	cfg.Faults = map[int]string{1: fixture.InjectExpired, 3: fixture.InjectReplay}
	results, _, err := pipeline.Verify(cfg)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	want := map[int]manifest.FailureReason{
		1: manifest.ReasonExpired,
		3: manifest.ReasonReplay,
	}
	for idx, reason := range want {
		if results[idx].FailureReason != reason {
			t.Fatalf("chunk %d reason = %q, want %q", idx, results[idx].FailureReason, reason)
		}
	}
}

// TestGraceWindowKeyExpired verifies overlap semantics: inside grace a stale
// round passes, beyond it the chunk is marked key_expired.
func TestGraceWindowKeyExpired(t *testing.T) {
	cfg, _ := buildHealthy(t, 4, 64, 2)
	cfg.Lic.NotAfter = cfg.Now.Unix() - 10
	cfg.Lic.GraceWindow = 3600
	if err := cfg.Lic.Sign(cfg.SigningKey); err != nil {
		t.Fatalf("resign: %v", err)
	}
	cfg.GraceWindow = time.Hour
	results, _, err := pipeline.Verify(cfg)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	for _, r := range results {
		if r.Status != manifest.StatusOK {
			t.Fatalf("expected grace coverage, chunk %d: %s", r.ChunkIndex, r.FailureReason)
		}
	}
	cfg.Faults = map[int]string{0: fixture.InjectWindow}
	results, _, err = pipeline.Verify(cfg)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if results[0].FailureReason != manifest.ReasonKeyExpired {
		t.Fatalf("want key_expired, got %q", results[0].FailureReason)
	}
}

var _ = license.License{}
