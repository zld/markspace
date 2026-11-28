package demo

import (
	"context"
	"strings"
	"testing"
)

func baseOptions() Options {
	return Options{Seed: 17, Duration: 60, Versions: 3, Window: 64, Buffer: 4, MemoryMB: 32, SlowMS: 0}
}

func TestRunMeetsAcceptanceThresholds(t *testing.T) {
	out, err := Run(context.Background(), baseOptions())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit = %d\n%s", out.ExitCode, out.String())
	}
	if out.Summary.Status.State != "committed" {
		t.Fatalf("state = %s", out.Summary.Status.State)
	}
	m := out.Metrics
	if ms := m.AVSyncErrorMS(); ms > 40 {
		t.Errorf("av_sync_error_ms = %d, want <= 40", ms)
	}
	if rate := m.LossRecoveryRate(); rate < 0.95 {
		t.Errorf("loss_recovery_rate = %.4f, want >= 0.95", rate)
	}
	if m.LatencyP95US <= 0 {
		t.Errorf("latency p95 = %d, want > 0", m.LatencyP95US)
	}
	if m.Missing == 0 {
		t.Error("fixture should itemise missing coverage")
	}
	if m.Conflicts == 0 {
		t.Error("fixture should exercise conflicts")
	}
	if m.UnresolvedConflicts != 0 {
		t.Errorf("unresolved = %d, want 0", m.UnresolvedConflicts)
	}
}

func TestRunIsDeterministic(t *testing.T) {
	first, err := Run(context.Background(), baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("manifest digest differs:\n%s\n%s", first.Digest, second.Digest)
	}
	if first.Metrics.LatencyP95US != second.Metrics.LatencyP95US {
		t.Fatalf("p95 latency differs: %d vs %d", first.Metrics.LatencyP95US, second.Metrics.LatencyP95US)
	}
	if first.String() != second.String() {
		t.Fatal("rendered report differs between identical runs")
	}
}

func TestDamagedFixturesReportAssetStageOffset(t *testing.T) {
	out, err := Run(context.Background(), baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	var truncations, recoveries, rejections int
	for _, c := range out.Damage {
		if !c.OK {
			t.Fatalf("damage case %s failed: %s", c.Name, c.Detail)
		}
		switch {
		case c.Name == "truncated-box":
			truncations++
			if c.Asset == "" || c.Stage == "" || c.Offset == 0 {
				t.Fatalf("corruption report lacks context: %+v", c)
			}
			if c.Kind != "corrupt" {
				t.Fatalf("kind = %s", c.Kind)
			}
		case c.Name == "missing-sample-table":
			rejections++
			if c.Asset == "" || c.Stage == "" || c.Offset == 0 {
				t.Fatalf("unrecoverable index report lacks context: %+v", c)
			}
		case c.Name == "bad-index":
			recoveries++
			if c.Detail != "digest matches clean scan" {
				t.Fatalf("recovery detail = %q", c.Detail)
			}
		}
	}
	if truncations == 0 || rejections == 0 || recoveries == 0 {
		t.Fatalf("damage coverage: truncations=%d rejections=%d recoveries=%d",
			truncations, rejections, recoveries)
	}
}

func TestFailureScenarioRollsBackAndReproduces(t *testing.T) {
	out, err := Run(context.Background(), baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	f := out.Failure
	if f.Status != "rolled_back" {
		t.Fatalf("status = %s", f.Status)
	}
	if !f.Consistent() {
		t.Fatalf("failing run is not reproducible: %+v", f)
	}
	if len(f.Records) == 0 {
		t.Fatal("rollback record missing")
	}
	rec := f.Records[0]
	if rec.Asset == "" || rec.Stage == "" || rec.Offset == 0 {
		t.Fatalf("rollback record lacks asset/stage/offset: %+v", rec)
	}
}

func TestRecoveryScenarioRescanMatches(t *testing.T) {
	out, err := Run(context.Background(), baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	r := out.Recovery
	if !r.Consistent() {
		t.Fatalf("rescan digest differs: %s vs %s", r.Digest, r.RescanDigest)
	}
	if r.Recovered == 0 {
		t.Fatal("expected a rebuilt index")
	}
	if r.Status != "committed" {
		t.Fatalf("status = %s", r.Status)
	}
}

func TestInvalidOptionsAreRejected(t *testing.T) {
	for _, o := range []Options{
		{Seed: 1, Duration: 10, Versions: 1, Window: 64, Buffer: 1, MemoryMB: 8},
		{Seed: 1, Duration: 0, Versions: 3, Window: 64, Buffer: 1, MemoryMB: 8},
		{Seed: 1, Duration: 10, Versions: 3, Window: 2, Buffer: 1, MemoryMB: 8},
		{Seed: 1, Duration: 10, Versions: 3, Window: 64, Buffer: 0, MemoryMB: 8},
	} {
		out, err := Run(context.Background(), o)
		if err == nil {
			t.Fatalf("options %+v accepted", o)
		}
		if out.Summary.Status.State != "rolled_back" {
			t.Fatalf("invalid options should roll back: %s", out.Summary.Status.State)
		}
		if out.ExitCode == 0 {
			t.Fatal("invalid options must not exit 0")
		}
	}
}

func TestReportContainsRequiredFields(t *testing.T) {
	out, err := Run(context.Background(), baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, field := range []string{
		"av_sync_error_ms=", "loss_recovery_rate=", "latency_p95_ms=",
		"missing=", "conflicts=", "rollback_status=",
	} {
		if !strings.Contains(text, field) {
			t.Errorf("report missing %q\n%s", field, text)
		}
	}
}

func TestPipelineFailureCarriesOneCause(t *testing.T) {
	out, err := Run(context.Background(), baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	// The failure scenario's rollback record and the damage fixture report
	// must describe the same underlying rejection: asset, stage and offset.
	var corrupt *damageCase
	for i := range out.Damage {
		if out.Damage[i].Name == "truncated-box" && out.Damage[i].Version == "v1" {
			corrupt = &out.Damage[i]
			break
		}
	}
	if corrupt == nil {
		t.Fatal("no truncated-box fixture reported")
	}
	rec := out.Failure.Records[0]
	if rec.Asset != corrupt.Asset || rec.Stage != corrupt.Stage || rec.Offset != corrupt.Offset {
		t.Fatalf("stage chain disagreed: record=%+v fixture=%+v", rec, corrupt)
	}
}

func TestMissingAndConflictCountsAreItemised(t *testing.T) {
	out, err := Run(context.Background(), baseOptions())
	if err != nil {
		t.Fatal(err)
	}
	var totalMissing, totalConflicts int
	for _, am := range out.Merges {
		totalMissing += am.Merged.MissingCount()
		totalConflicts += am.Merged.ConflictCount()
		for _, mi := range am.Merged.Missing {
			if mi.TrackID == "" || mi.Version == "" || mi.Reason == "" {
				t.Fatalf("missing item lacks identity: %+v", mi)
			}
			if mi.Span.End <= mi.Span.Start {
				t.Fatalf("missing item has empty span: %+v", mi)
			}
		}
		for _, c := range am.Merged.Conflicts {
			if c.TrackID == "" || len(c.Details) < 2 {
				t.Fatalf("conflict item lacks detail: %+v", c)
			}
			if c.Resolved && c.Chosen == "" {
				t.Fatalf("resolved conflict did not record the selected version: %+v", c)
			}
		}
	}
	if totalMissing != out.Metrics.Missing || totalConflicts != out.Metrics.Conflicts {
		t.Fatalf("metric mismatch: missing %d/%d conflicts %d/%d",
			totalMissing, out.Metrics.Missing, totalConflicts, out.Metrics.Conflicts)
	}
}

func TestMemoryBudgetEnforced(t *testing.T) {
	// A long fixture at a 1 MiB budget must refuse the sample table rather
	// than grow without bound, and must roll the run back with exit code 2.
	o := Options{Seed: 17, Duration: 600, Versions: 2, Window: 64, Buffer: 4, MemoryMB: 1}
	out, err := Run(context.Background(), o)
	if err == nil {
		t.Fatal("1 MiB budget should refuse a 600s fixture's sample table")
	}
	if !strings.Contains(err.Error(), "memory limit") {
		t.Fatalf("error does not name the budget: %v", err)
	}
	if out == nil || out.Summary.Status.State != "rolled_back" {
		t.Fatalf("unexpected state: %+v", out)
	}
	if out.ExitCode != 2 {
		t.Fatalf("exit = %d", out.ExitCode)
	}
}
