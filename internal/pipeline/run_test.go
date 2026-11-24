package pipeline

import (
	"testing"

	"mediacheck/internal/fixture"
)

func runCase(t *testing.T, name fixture.CaseName) *Outcome {
	t.Helper()
	c := fixture.BuildCase(name, 23)
	out, err := Run(c, t.TempDir())
	if err != nil {
		t.Fatalf("Run(%s): %v", name, err)
	}
	return out
}

func TestNormalCaseHasZeroDamage(t *testing.T) {
	out := runCase(t, fixture.CaseNormal)
	if len(out.Metrics.DamageRanges) != 0 {
		t.Fatalf("normal case must have zero damage ranges, got %d: %+v",
			len(out.Metrics.DamageRanges), out.Metrics.DamageErrors)
	}
	if !out.Metrics.Tracks[0].Monotonic {
		t.Fatal("normal timeline must be monotonic")
	}
	if out.Metrics.P95LatencyMs <= 0 || out.Metrics.P95LatencyMs >= 100 {
		t.Fatalf("p95 latency = %d ms, want 0 < p95 < 100", out.Metrics.P95LatencyMs)
	}
}

func TestWrapDriftTimelineMonotonicAndSyncWithin20ms(t *testing.T) {
	out := runCase(t, fixture.CaseWrapDrift)
	for _, l := range out.Lines {
		if !l.Monotonic {
			t.Fatalf("track %d wrapped timeline must stay monotonic", l.TrackID)
		}
	}
	if out.RTP == nil {
		t.Fatal("wrap case must run RTP recovery")
	}
	for i := 1; i < len(out.RTP.Timeline); i++ {
		if out.RTP.Timeline[i] < out.RTP.Timeline[i-1] {
			t.Fatal("unwrapped RTP timeline must be non-decreasing across the rollover")
		}
	}
	// The injected 120 ppm drift must be quantified, not silently ignored.
	if out.RTP.Drift.PPM < 110 || out.RTP.Drift.PPM > 130 {
		t.Fatalf("quantified drift = %.2f PPM, want ~120", out.RTP.Drift.PPM)
	}
	if out.Sync == nil || out.Sync.AbsMeanMs > 20 {
		t.Fatalf("A/V sync abs mean = %v, want <= 20 ms", out.Sync)
	}
}

func TestVFRCasePreservesNonConstantGaps(t *testing.T) {
	out := runCase(t, fixture.CaseVFR)
	// The video line must contain at least two distinct positive frame gaps.
	for _, l := range out.Lines {
		if l.Kind.String() != "video" {
			continue
		}
		gaps := l.Gaps()
		seen := map[int64]bool{}
		for _, g := range gaps {
			seen[g.GapNanos] = true
		}
		if len(seen) < 2 {
			t.Fatalf("VFR must preserve multiple distinct intervals, got %d", len(seen))
		}
	}
}

func TestTruncatedCaseLocalizesTailRange(t *testing.T) {
	out := runCase(t, fixture.CaseTruncated)
	if !out.Metrics.HasUnrecoverable && !hasUndecidable(out) {
		t.Fatal("truncated case must localize a tail/undecidable range")
	}
	found := false
	for _, e := range out.Damages {
		if e.Code == "index_undecidable" || e.Code == "truncated_box" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected truncation/index damage, got %+v", out.Metrics.DamageErrors)
	}
}

func TestCrossTrackDamageAttributesBothTracks(t *testing.T) {
	out := runCase(t, fixture.CaseCrossTrack)
	tracks := map[uint32]bool{}
	for _, e := range out.Damages {
		if e.TrackID != 0 {
			tracks[e.TrackID] = true
		}
	}
	if !tracks[1] || !tracks[2] {
		t.Fatalf("cross-track damage must hit both tracks, got %+v", tracks)
	}
}

func TestRTPLossRecoveryAtLeast90Percent(t *testing.T) {
	out := runCase(t, fixture.CaseRTPLoss)
	if out.RTP == nil {
		t.Fatal("RTP case must produce a recovery result")
	}
	if out.Metrics.LossRecoveryRate < 0.90 {
		t.Fatalf("recovery rate = %.3f, want >= 0.90", out.Metrics.LossRecoveryRate)
	}
	if out.Metrics.RecoveredPackets == 0 {
		t.Fatal("recovered packet count must be positive")
	}
}

func TestRepeatRunProducesIdenticalSummary(t *testing.T) {
	a := runCase(t, fixture.CaseRTPLoss)
	b := runCase(t, fixture.CaseRTPLoss)
	if a.Metrics.P95LatencyMs != b.Metrics.P95LatencyMs {
		t.Fatal("p95 latency must be deterministic across runs")
	}
	if a.Metrics.LossRecoveryRate != b.Metrics.LossRecoveryRate {
		t.Fatal("recovery rate must be deterministic across runs")
	}
	if len(a.Metrics.DamageRanges) != len(b.Metrics.DamageRanges) {
		t.Fatal("damage range count must be deterministic across runs")
	}
	for i := range a.RTP.Timeline {
		if a.RTP.Timeline[i] != b.RTP.Timeline[i] {
			t.Fatal("RTP timeline must be identical across repeated runs")
		}
	}
}

func TestAllCasesP95Under100ms(t *testing.T) {
	for _, name := range fixture.AllCases {
		out := runCase(t, name)
		if out.Metrics.P95LatencyMs >= 100 {
			t.Errorf("case %s p95 latency = %d ms, want < 100", name, out.Metrics.P95LatencyMs)
		}
	}
}

func hasUndecidable(out *Outcome) bool {
	for _, tr := range out.Metrics.Tracks {
		if tr.Undecidable {
			return true
		}
	}
	return false
}
