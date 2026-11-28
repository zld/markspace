package config

import (
	"path/filepath"
	"testing"

	"avpipe/internal/fixture"
	"avpipe/internal/model"
	"avpipe/internal/repair"
	"avpipe/internal/scan"
)

func build(t *testing.T, name string) (*repair.Report, *scan.Index) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := fixture.WriteCase(dir, fixture.Cases()[name]); err != nil {
		t.Fatal(err)
	}
	idx, err := scan.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	return repair.Repair(idx), idx
}

func TestDeriveFeasible(t *testing.T) {
	rep, idx := build(t, "normal")
	c, err := Derive(rep, idx, 900, 80, 800)
	if err != nil || !c.Feasible {
		t.Fatalf("expected feasible, got err=%v reasons=%v", err, c.Reasons)
	}
	if c.Kbps > 900 {
		t.Fatalf("kbps=%d exceeds 900", c.Kbps)
	}
	if c.BudgetMS > 80 {
		t.Fatalf("budget_ms=%d exceeds 80", c.BudgetMS)
	}
	if c.CoveragePermille < 800 {
		t.Fatalf("coverage=%d below target", c.CoveragePermille)
	}
	if len(c.Decisions) != 3 {
		t.Fatalf("want 3 decisions, got %d", len(c.Decisions))
	}
	for _, d := range c.Decisions {
		if d.Measured == "" || d.Constraint == "" || d.Reason == "" {
			t.Fatalf("decision missing evidence: %+v", d)
		}
	}
	if c.Explain() == "" {
		t.Fatal("explain produced no output")
	}
}

func TestDeriveBitrateInfeasible(t *testing.T) {
	rep, idx := build(t, "normal")
	ok, _ := Derive(rep, idx, 900, 80, 800)
	capKbps := ok.MeasuredKbps - 1 // strictly below the measured requirement
	c, err := Derive(rep, idx, capKbps, 80, 800)
	if err == nil || c.Feasible {
		t.Fatal("a cap below measured bitrate must be infeasible")
	}
	if me, ok := model.AsError(err); !ok || me.Code != model.CodeInfeasible {
		t.Fatalf("want infeasible error, got %v", err)
	}
	if len(c.Reasons) == 0 {
		t.Fatal("infeasible config must state reasons")
	}
}

func TestDeriveDeadlineInfeasible(t *testing.T) {
	rep, idx := build(t, "normal")
	c, err := Derive(rep, idx, 900, 0, 800)
	if err == nil || c.Feasible {
		t.Fatal("zero processing deadline must be infeasible")
	}
	if len(c.Reasons) == 0 {
		t.Fatal("missing infeasibility reason")
	}
}

func TestDamagedCoverageBelowFull(t *testing.T) {
	rep, idx := build(t, "damaged")
	// A stopped track loses samples, so demanding 100% coverage is infeasible.
	c, err := Derive(rep, idx, 900, 80, 1000)
	if err == nil || c.Feasible {
		t.Fatal("damaged input cannot reach 100% coverage")
	}
	if c.KeptSamples >= c.TotalSamples {
		t.Fatal("kept samples must be fewer than declared when a track stops")
	}
}
