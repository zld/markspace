// Package integration holds end-to-end tests that span scanner, alignment and
// recovery paths in one scenario, proving the same fixtures and unified error
// model hold across stage boundaries.
package integration

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/fixture"
	"mediapack/internal/index"
	"mediapack/internal/ladder"
	"mediapack/internal/manifest"
	"mediapack/internal/model"
	"mediapack/internal/probe"
	"mediapack/internal/runner"
	"mediapack/internal/timeline"
)

// buildPlan runs scan -> index -> timeline -> ladder for the given bundles.
func buildPlan(t *testing.T, work string, bundles []fixture.Bundle, segNS int64) (*manifest.Plan, []runner.InputRef) {
	t.Helper()
	store, err := index.NewStore(filepath.Join(work, "idx"))
	if err != nil {
		t.Fatal(err)
	}
	tables := make([]*timeline.ClockTable, 0, len(bundles))
	inputs := make([]runner.InputRef, 0, len(bundles))
	for _, b := range bundles {
		entry, ierr := store.GetOrScan(b.Container, probe.DefaultLimits())
		if ierr != nil {
			t.Fatalf("scan: %v", ierr)
		}
		inputs = append(inputs, runner.InputRef{Path: b.Container, SHA256: entry.Summary.SHA256})
		raw, _ := os.ReadFile(b.RTPSidecar)
		pkts, _, perr := timeline.ParseRTPPacketStream(raw)
		if perr != nil {
			t.Fatalf("rtp: %v", perr)
		}
		tables = append(tables, &timeline.ClockTable{
			TrackID: b.Spec.ID, ClockRate: b.Spec.ClockRate,
			BaseTS: pkts[0].Timestamp, Packets: pkts,
		})
	}
	var es model.Errors
	mt, aerr := timeline.Align(tables, nil, &es)
	if aerr != nil {
		t.Fatalf("align: %v", aerr)
	}
	var le model.Errors
	res, lerr := ladder.Align(
		[]ladder.Representation{{ID: "r0", Timeline: mt}},
		ladder.Config{TargetSegmentNS: segNS, ToleranceNS: segNS / 4, MaxRenditions: 8}, &le)
	if lerr != nil {
		t.Fatalf("ladder: %v", lerr)
	}
	return res.Plans[res.Aligned[0]], inputs
}

// Fault class 1: a truncated box is localized at scan time, then after the
// healthy input is reprocessed, a corrupt checkpoint forces recovery to the
// safe boundary — spanning probe and runner in one scenario.
func TestTruncatedBoxThenCheckpointRecovery(t *testing.T) {
	work := t.TempDir()
	bundles, err := fixture.Generate(filepath.Join(work, "in"), fixture.DefaultSpecs(2))
	if err != nil {
		t.Fatal(err)
	}
	// --- scan path: truncated box localized with byte offset ---
	f, ferr := fixture.InjectTruncatedBox(bundles[0].Container, filepath.Join(work, "corrupt.mp4"))
	if ferr != nil {
		t.Fatal(ferr)
	}
	_, serr := probe.Scan(f.Path, probe.DefaultLimits())
	pe, ok := model.AsPipeline(serr)
	if !ok || pe.Code != model.CodeTruncatedBox || pe.ByteRange.Offset != f.Offset {
		t.Fatalf("scan path localization failed: %v", serr)
	}

	// --- align + runner path on the healthy inputs ---
	plan, inputs := buildPlan(t, work, bundles, 2_000_000_000)
	exec := filepath.Join(work, "exec")
	full, e1 := runner.Execute(plan, inputs, runner.Config{WorkDir: exec}, 0)
	if e1 != nil {
		t.Fatal(e1)
	}
	// Corrupt the checkpoint: recovery must roll back and still reproduce the
	// exact original output digest.
	cpPath := filepath.Join(exec, "checkpoint.json")
	if err := os.WriteFile(cpPath, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, e2 := runner.Execute(plan, inputs, runner.Config{WorkDir: exec}, 0)
	if e2 != nil {
		t.Fatalf("recovery: %v", e2)
	}
	if recovered.CheckpointReused || recovered.OutputSHA256 != full.OutputSHA256 {
		t.Fatalf("recovery invariant broken: reused=%v sameDigest=%v",
			recovered.CheckpointReused, recovered.OutputSHA256 == full.OutputSHA256)
	}
}

// Fault class 2: an unalignable configuration is rejected at the align/ladder
// path with a structured tolerance error, and no manifest is produced; then a
// corrupt segment forces safe-prefix rollback on the runner path.
func TestUnalignableThenSegmentRollback(t *testing.T) {
	work := t.TempDir()
	bundles, err := fixture.Generate(filepath.Join(work, "in"), fixture.DefaultSpecs(2))
	if err != nil {
		t.Fatal(err)
	}
	store, _ := index.NewStore(filepath.Join(work, "idx"))
	tables := make([]*timeline.ClockTable, 0, len(bundles))
	for _, b := range bundles {
		if _, ierr := store.GetOrScan(b.Container, probe.DefaultLimits()); ierr != nil {
			t.Fatal(ierr)
		}
		raw, _ := os.ReadFile(b.RTPSidecar)
		pkts, _, _ := timeline.ParseRTPPacketStream(raw)
		tables = append(tables, &timeline.ClockTable{
			TrackID: b.Spec.ID, ClockRate: b.Spec.ClockRate,
			BaseTS: pkts[0].Timestamp, Packets: pkts,
		})
	}
	var es model.Errors
	mt, aerr := timeline.Align(tables, nil, &es)
	if aerr != nil {
		t.Fatal(aerr)
	}
	// --- alignment path: impossible tolerance must reject ---
	var le model.Errors
	_, lerr := ladder.Align(
		[]ladder.Representation{{ID: "r0", Timeline: mt}},
		ladder.Config{TargetSegmentNS: 1_500_000_000, ToleranceNS: 1, MaxRenditions: 8}, &le)
	pe, ok := model.AsPipeline(lerr)
	if !ok || pe.Code != model.CodeAlignTolerance {
		t.Fatalf("alignment path must reject: %v", lerr)
	}

	// --- runner path: a valid plan then safe-prefix rollback ---
	plan, inputs := buildPlan(t, filepath.Join(work, "p2"), bundles, 2_000_000_000)
	exec := filepath.Join(work, "exec2")
	full, e1 := runner.Execute(plan, inputs, runner.Config{WorkDir: exec}, 0)
	if e1 != nil {
		t.Fatal(e1)
	}
	if err := os.WriteFile(full.Segments[0].Path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, e2 := runner.Execute(plan, inputs, runner.Config{WorkDir: exec}, 0)
	if e2 != nil || recovered.OutputSHA256 != full.OutputSHA256 {
		t.Fatalf("safe-prefix rollback invariant failed: %v", e2)
	}
	if recovered.ResumedFrom != 0 {
		t.Fatalf("first-segment corruption must roll back to boundary 0, got %d", recovered.ResumedFrom)
	}
}
