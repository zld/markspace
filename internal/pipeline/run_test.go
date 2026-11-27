package pipeline

import (
	"encoding/json"
	"testing"
	"time"

	"mediarc/internal/model"
)

func TestRunHealthyScenarioDeterministic(t *testing.T) {
	cfg := Config{Seed: 17, DriftMs: 4, BoundaryMs: 20, BudgetMs: 100000}
	s1, recs1, _, _ := Run(cfg)
	s2, _, _, _ := Run(cfg)

	if s1.RepresentationCount < 5 {
		t.Fatalf("expected full fixture set, got %d reps", s1.RepresentationCount)
	}
	// Determinism: same seed and params must yield identical counts and
	// per-item conclusions across runs.
	if s1.RecoveredCount != s2.RecoveredCount ||
		s1.AnomalyCount != s2.AnomalyCount ||
		s1.FingerprintMatches != s2.FingerprintMatches {
		t.Fatal("repeated runs must produce identical summary fields")
	}
	for i := range s1.Conclusions {
		if s1.Conclusions[i].Conclusion != s2.Conclusions[i].Conclusion ||
			s1.Conclusions[i].RepID != s2.Conclusions[i].RepID {
			t.Fatalf("conclusion %d not deterministic", i)
		}
	}
	// Raw timestamps must be preserved through recovery.
	for _, rec := range recs1 {
		for _, rt := range rec.Tracks {
			if len(rt.RawTimes) != len(rt.Track.Samples) {
				t.Fatalf("%s raw timestamp count mismatch", rec.ID)
			}
			for i, s := range rt.Track.Samples {
				if rt.RawTimes[i] != s.Dts {
					t.Fatalf("%s raw timestamp overwritten at %d", rec.ID, i)
				}
			}
		}
	}
	// Healthy reps recover; the two synthetic failures do not.
	status := map[string]bool{}
	for _, c := range s1.Conclusions {
		status[c.RepID] = c.Recovered
	}
	if !status["dash-720p"] || !status["hls-variant-a"] || !status["dash-wrap"] {
		t.Fatalf("healthy reps must recover: %+v", status)
	}
	if status["dash-corrupt"] || status["hls-truncated"] {
		t.Fatalf("synthetic failures must not be marked recovered: %+v", status)
	}
	// The corrupt rep must not erase the healthy rep's fingerprints.
	if s1.FingerprintMatches < 2 {
		t.Fatalf("healthy representations must keep matching fingerprints, got %d", s1.FingerprintMatches)
	}
}

func TestRunWraparoundExpanded(t *testing.T) {
	cfg := Config{Seed: 17, DriftMs: 4, BoundaryMs: 20, BudgetMs: 100000}
	_, recs, _, _ := Run(cfg)
	for _, rec := range recs {
		if rec.ID != "dash-wrap" {
			continue
		}
		wraps := 0
		for _, rt := range rec.Tracks {
			wraps += rt.WrapCount
		}
		if wraps == 0 {
			t.Fatal("wraparound fixture must expand at least one carrier wrap")
		}
		found := false
		for _, e := range rec.Errors {
			if e.Kind == model.KindTimestampWrap {
				found = true
			}
		}
		if !found {
			t.Fatal("wraparound expansion must be reported as an informational error")
		}
		return
	}
	t.Fatal("dash-wrap representation missing")
}

func TestRunTightThresholdsFlagAnomaliesAndBudget(t *testing.T) {
	// boundary 1ms / budget 1ms forces boundary threshold and budget breach.
	// A deterministic simulated elapsed keeps the budget verdict reproducible
	// regardless of machine speed.
	cfg := Config{Seed: 17, DriftMs: 4, BoundaryMs: 1, BudgetMs: 1,
		SimulatedElapsed: 5 * time.Millisecond}
	s, _, _, _ := Run(cfg)
	if !s.BudgetExceeded || s.BudgetDeviationMs != 4 {
		t.Fatalf("tight budget must report overrun: %+v", s)
	}
	if s.BoundaryThresholdMs != 1 {
		t.Fatal("boundary threshold must propagate to the summary")
	}
	if s.AnomalyCount == 0 {
		t.Fatal("1ms boundary threshold must surface at least one anomaly")
	}
}

func TestRunDriftExceededClassified(t *testing.T) {
	cfg := Config{Seed: 17, DriftMs: 4, BoundaryMs: 20, BudgetMs: 100000}
	s, _, _, _ := Run(cfg)
	for _, c := range s.Conclusions {
		if c.RepID == "hls-drift" {
			if !c.DriftExceeded {
				t.Fatalf("8%% slope drift must exceed the %d ms/s threshold", cfg.DriftMs)
			}
			return
		}
	}
	t.Fatal("drift representation missing")
}

// TestIntegrityGuardSemantics mirrors the CLI exit-code-3 rule: a recovered,
// non-review item whose fingerprints do not match is an integrity failure,
// whereas the wrapping timeline is held for review and is not a failure.
func TestIntegrityGuardSemantics(t *testing.T) {
	cfg := Config{Seed: 17, DriftMs: 4, BoundaryMs: 20, BudgetMs: 100000}
	s, _, _, _ := Run(cfg)
	for _, c := range s.Conclusions {
		integrityFailure := c.Conclusion == "remux_mismatch" && c.Recovered && !c.ReviewRequired
		if integrityFailure {
			t.Fatalf("recovered non-review item %s must not fail integrity: %+v", c.RepID, c)
		}
		// The wrap item is a known linear-container limit, not corruption.
		if c.RepID == "dash-wrap" && c.Conclusion != "review_required" {
			t.Fatalf("wrap item must be review_required, got %s", c.Conclusion)
		}
	}
}

// TestReportSerializes guarantees the CLI's JSON encoding path (whose failure
// maps to exit code 3) always succeeds for a real pipeline summary.
func TestReportSerializes(t *testing.T) {
	s, _, _, _ := Run(Config{Seed: 17, DriftMs: 4, BoundaryMs: 20, BudgetMs: 100000})
	if _, err := json.Marshal(s); err != nil {
		t.Fatalf("summary must be JSON-serializable: %v", err)
	}
}
