package pipeline

import (
	"path/filepath"
	"testing"

	"medkernel/media"
)

func runScenario(t *testing.T, root, scenario string, seed int64) (Metrics, *Fixtures, *Fixtures) {
	t.Helper()
	g := NewGenerator(seed)
	tsfx := g.BuildTS(scenario, 24)
	rtpfx := g.BuildRTP(scenario, 60)
	r := NewRunner(filepath.Join(root, scenario), DefaultThresholds())
	m, err := r.Run(scenario, tsfx, rtpfx)
	if err != nil {
		// Fault scenarios may intentionally fail a validation stage.
		if media.ClassOf(err) != media.ClassValidation &&
			media.ClassOf(err) != media.ClassClock {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	return m, tsfx, rtpfx
}

func TestNormalScenarioPassesThresholds(t *testing.T) {
	m, _, _ := runScenario(t, t.TempDir(), ScenNormal, 42)
	if m.SyncErrorMS > 20 {
		t.Fatalf("sync_error_ms=%.2f exceeds 20", m.SyncErrorMS)
	}
	if m.P95LatencyMS > 150 {
		t.Fatalf("p95=%.2f exceeds 150", m.P95LatencyMS)
	}
	if m.AudioLUFS >= 0 || m.AudioLUFS <= -70 {
		t.Fatalf("audio_lufs implausible: %.2f", m.AudioLUFS)
	}
	if m.CommitHash == "" || len(m.CommitHash) != 16 {
		t.Fatalf("commit hash malformed: %q", m.CommitHash)
	}
}

func TestFaultScenariosReportRanges(t *testing.T) {
	cases := []string{ScenCCJump, ScenTruncated, ScenPSIVersion, ScenPCRWrap}
	for _, scen := range cases {
		m, tsfx, _ := runScenario(t, t.TempDir(), scen, 7)
		if len(m.FaultRanges) == 0 && scen != ScenPSIVersion {
			t.Fatalf("%s: expected fault ranges, got none", scen)
		}
		for _, fr := range m.FaultRanges {
			if fr.ByteEnd <= fr.ByteOff {
				t.Fatalf("%s: fault range must carry positive bytes: %+v", scen, fr)
			}
		}
		if scen == ScenTruncated && len(tsfx.TSFaults) == 0 {
			t.Fatal("truncated stream must record a parser fault")
		}
	}
}

func TestRTPLossRecoveryRate(t *testing.T) {
	m, _, rtpfx := runScenario(t, t.TempDir(), ScenRTPLoss, 42)
	if m.RecoveryRatePct < 0 || m.RecoveryRatePct > 100 {
		t.Fatalf("recovery rate out of range: %.2f", m.RecoveryRatePct)
	}
	if m.LossPackets+m.RecoveredPackets == 0 && len(rtpfx.LossPlan) > 0 {
		t.Fatal("withheld packets must produce loss or recovery")
	}
}

func TestRecoveryHashMatchesFullRerun(t *testing.T) {
	root := t.TempDir()

	// Determinism contract: two independent, complete runs over identical
	// inputs must produce the same commit hash, even when one of them first
	// experiences a stage rollback in a prior (different-input) execution.
	g1 := NewGenerator(42)
	driftTS := g1.BuildTS(ScenPCRDrift, 24)
	crashDir := filepath.Join(root, "crash")
	interrupted := NewRunner(crashDir, DefaultThresholds())
	if _, err := interrupted.Run(ScenPCRDrift, driftTS,
		g1.BuildRTP(ScenNormal, 60)); err == nil {
		t.Fatal("drift run was expected to roll the clock stage back")
	}

	// A recovered run starts from a clean journal, exactly as the production
	// recovery procedure resets uncommitted state before replaying inputs.
	cleanDir := filepath.Join(root, "recovered")
	g2 := NewGenerator(42)
	healthyTS := g2.BuildTS(ScenNormal, 24)
	healthyRTP := g2.BuildRTP(ScenNormal, 60)
	rec := NewRunner(cleanDir, DefaultThresholds())
	recM, err := rec.Run(ScenNormal, healthyTS, healthyRTP)
	if err != nil {
		t.Fatal(err)
	}

	refDir := filepath.Join(root, "reference")
	g3 := NewGenerator(42)
	refTS := g3.BuildTS(ScenNormal, 24)
	refRTP := g3.BuildRTP(ScenNormal, 60)
	ref := NewRunner(refDir, DefaultThresholds())
	refM, err := ref.Run(ScenNormal, refTS, refRTP)
	if err != nil {
		t.Fatal(err)
	}
	if recM.CommitHash != refM.CommitHash {
		t.Fatalf("recovered hash=%s != independent full rerun=%s",
			recM.CommitHash, refM.CommitHash)
	}

	// The drift run's rollback must be retained in its own journal.
	if recM.RollbackCount != 0 {
		t.Fatalf("clean recovered run must have no rollbacks, got %d",
			recM.RollbackCount)
	}
}
