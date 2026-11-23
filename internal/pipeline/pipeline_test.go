package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"archivecheck/internal/fixture"
	"archivecheck/internal/model"
)

// runFault generates a fixture in a temp dir and runs the pipeline on it.
func runFault(t *testing.T, fault fixture.Fault, mutate func(*Config)) *Report {
	t.Helper()
	root := t.TempDir()
	if _, err := fixture.Generate(fixture.Config{Seed: 42, Root: root, Fault: fault}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	cfg := Config{Root: root, Concurrency: 4}
	if mutate != nil {
		mutate(&cfg)
	}
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return rep
}

func TestCleanArchiveCertifies(t *testing.T) {
	rep := runFault(t, fixture.FaultNone, nil)
	if rep.ExitCode != ExitOK {
		t.Fatalf("exit=%d faults=%v", rep.ExitCode, rep.Faults)
	}
	if rep.ManifestSegments != fixture.DefaultSegments {
		t.Fatalf("manifest_segments=%d want %d", rep.ManifestSegments, fixture.DefaultSegments)
	}
	if rep.Representations != 2 || rep.Tracks != 1 {
		t.Fatalf("reps=%d tracks=%d", rep.Representations, rep.Tracks)
	}
	if rep.KeyframeP50Ms != 1000 {
		t.Fatalf("keyframe p50=%d", rep.KeyframeP50Ms)
	}
	if len(rep.DamageRanges) != 0 || rep.FirstBadSegment != "" {
		t.Fatalf("clean archive has damage %+v", rep.DamageRanges)
	}
	if rep.OutputSHA256 == "" || len(rep.Segments) != 2*fixture.DefaultSegments {
		t.Fatalf("digest=%q segments=%d", rep.OutputSHA256, len(rep.Segments))
	}
	for _, a := range rep.Reps {
		if !a.Aligned {
			t.Fatalf("representation %s not aligned", a.ID)
		}
	}
}

func TestBurstLossPropagatesToCrossBoundaryDamage(t *testing.T) {
	rep := runFault(t, fixture.FaultBurstLoss, nil)
	if rep.MissingPackets == 0 {
		t.Fatal("burst loss must be counted at the transport layer")
	}
	if rep.ExitCode != ExitDamage {
		t.Fatalf("exit=%d want %d", rep.ExitCode, ExitDamage)
	}
	if len(rep.DamageRanges) != 1 {
		t.Fatalf("want one merged range, got %+v", rep.DamageRanges)
	}
	dr := rep.DamageRanges[0]
	if len(dr.Segments) != 2 {
		t.Fatalf("range should span two segments, got %v", dr.Segments)
	}
	if dr.End <= dr.Start {
		t.Fatalf("range %+v", dr)
	}
	if rep.FirstBadSegment == "" {
		t.Fatal("first bad segment must be identified")
	}
	if rep.FirstBlocked == "" {
		t.Fatal("damage should block certification")
	}
}

func TestTruncatedBoxBlocksDAG(t *testing.T) {
	rep := runFault(t, fixture.FaultTruncatedBox, nil)
	if rep.ExitCode != ExitDamage {
		t.Fatalf("exit=%d", rep.ExitCode)
	}
	if rep.FirstBadSegment != "7" {
		t.Fatalf("first bad segment=%q want 7", rep.FirstBadSegment)
	}
	if rep.FirstBlocked == "" {
		t.Fatal("a truncated segment must block downstream nodes")
	}
	if len(rep.DamageRanges) != 1 || rep.DamageRanges[0].Confidence != "estimated" {
		t.Fatalf("damage %+v", rep.DamageRanges)
	}
	// At least one node must have failed with the truncation code.
	sawTruncated := false
	for _, f := range rep.Faults {
		if f.Code == model.CodeMP4BoxTruncated {
			sawTruncated = true
		}
	}
	if !sawTruncated {
		t.Fatalf("expected a box truncation fault, got %v", rep.Faults)
	}
}

func TestDuplicateReorderPreservesOutput(t *testing.T) {
	rep := runFault(t, fixture.FaultDuplicateReorder, nil)
	if rep.DuplicatePackets == 0 {
		t.Fatal("duplicates must be counted")
	}
	if rep.MissingPackets != 0 {
		t.Fatalf("reordering must not lose packets, missing=%d", rep.MissingPackets)
	}
	if len(rep.DamageRanges) != 0 {
		t.Fatalf("duplicates alone are not damage: %+v", rep.DamageRanges)
	}
	if rep.ExitCode != ExitOK {
		t.Fatalf("exit=%d faults=%v", rep.ExitCode, rep.Faults)
	}
}

func TestResumeReusesEveryNodeAndMatchesChecksum(t *testing.T) {
	root := t.TempDir()
	if _, err := fixture.Generate(fixture.Config{Seed: 42, Root: root, Fault: fixture.FaultBurstLoss}); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(root, "state", "cp.json")
	first, err := Run(context.Background(), Config{Root: root, Concurrency: 2, CheckpointPath: cp,
		Seed: 42, FaultName: string(fixture.FaultBurstLoss)})
	if err != nil {
		t.Fatal(err)
	}
	if first.ResumedNodes != 0 {
		t.Fatalf("first run resumed=%d", first.ResumedNodes)
	}
	second, err := Run(context.Background(), Config{Root: root, Concurrency: 2, CheckpointPath: cp, Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	// Without a run record the resume still re-executes nothing because the
	// timebase is normalised into the node keys.
	if second.ResumedNodes != 0 {
		t.Fatalf("resume re-executed %d nodes", second.ResumedNodes)
	}
	if second.CachedNodes != second.DAGNodes {
		t.Fatalf("cached %d of %d nodes", second.CachedNodes, second.DAGNodes)
	}
	if second.OutputSHA256 != first.OutputSHA256 {
		t.Fatalf("checksum changed across resume: %s vs %s", first.OutputSHA256, second.OutputSHA256)
	}
	if second.ExitCode != first.ExitCode {
		t.Fatalf("exit changed: %d vs %d", first.ExitCode, second.ExitCode)
	}
	if len(second.DamageRanges) != len(first.DamageRanges) {
		t.Fatalf("damage changed across resume: %+v vs %+v", first.DamageRanges, second.DamageRanges)
	}
	if _, err := os.Stat(cp); err != nil {
		t.Fatalf("checkpoint missing: %v", err)
	}
}

func TestCorruptCheckpointRefusesResume(t *testing.T) {
	root := t.TempDir()
	if _, err := fixture.Generate(fixture.Config{Seed: 1, Root: root, Fault: fixture.FaultNone}); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(root, "cp.json")
	if _, err := Run(context.Background(), Config{Root: root, CheckpointPath: cp}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside the digest so the store's verification trips.
	for i := range data {
		if string(data[i:i+9]) == `"digest":` {
			data[i+12] ^= 0xff
			break
		}
	}
	if err := os.WriteFile(cp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), Config{Root: root, CheckpointPath: cp, Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.ExitCode != ExitBlocked || !rep.CheckpointMismatch {
		t.Fatalf("exit=%d mismatch=%v", rep.ExitCode, rep.CheckpointMismatch)
	}
}

func TestManifestInputErrorsExitTwo(t *testing.T) {
	cases := map[string]fixture.Fault{
		"inexact":  fixture.FaultInexactTimebase,
		"boundary": fixture.FaultManifestBoundary,
	}
	for name, fault := range cases {
		t.Run(name, func(t *testing.T) {
			rep := runFault(t, fault, nil)
			if rep.ExitCode != ExitInput {
				t.Fatalf("exit=%d want 2", rep.ExitCode)
			}
			sawPosition := false
			for _, f := range rep.Faults {
				if f.Source.Line > 0 && f.Source.File != "" {
					sawPosition = true
				}
			}
			if !sawPosition {
				t.Fatalf("manifest fault lacks source position: %v", rep.Faults)
			}
		})
	}
}

func TestInexactCarriedWhenAllowed(t *testing.T) {
	rep := runFault(t, fixture.FaultInexactTimebase, func(c *Config) { c.AllowInexact = true })
	if rep.ExitCode == ExitInput {
		t.Fatalf("allow-inexact should not be an input error: %v", rep.Faults)
	}
	if len(rep.Segments) == 0 {
		t.Fatal("expected at least one segment")
	}
}

func TestKeyframeSkewFlagsRepresentation(t *testing.T) {
	rep := runFault(t, fixture.FaultKeyframeSkew, nil)
	var low *RepAudit
	for i := range rep.Reps {
		if rep.Reps[i].ID == "v-low" {
			low = &rep.Reps[i]
		}
	}
	if low == nil {
		t.Fatal("v-low missing")
	}
	if low.Aligned {
		t.Fatalf("v-low should be unaligned: %+v", low)
	}
	if rep.ExitCode != ExitDamage {
		t.Fatalf("exit=%d", rep.ExitCode)
	}
}

func TestDigestConflictFlagsRepresentation(t *testing.T) {
	rep := runFault(t, fixture.FaultDigestConflict, nil)
	if rep.ExitCode != ExitDamage {
		t.Fatalf("exit=%d", rep.ExitCode)
	}
	sawDigest := false
	for _, f := range rep.Faults {
		if f.Code == model.CodeDamageRepresentation {
			sawDigest = true
		}
	}
	if !sawDigest {
		t.Fatalf("expected a representation digest fault, got %v", rep.Faults)
	}
}

func TestDeterministicAcrossConcurrency(t *testing.T) {
	root := t.TempDir()
	if _, err := fixture.Generate(fixture.Config{Seed: 42, Root: root, Fault: fixture.FaultNone}); err != nil {
		t.Fatal(err)
	}
	var digests []string
	for _, conc := range []int{1, 4, 8} {
		rep, err := Run(context.Background(), Config{Root: root, Concurrency: conc})
		if err != nil {
			t.Fatal(err)
		}
		digests = append(digests, rep.OutputSHA256)
	}
	for i := 1; i < len(digests); i++ {
		if digests[i] != digests[0] {
			t.Fatalf("output digest depends on concurrency: %v", digests)
		}
	}
}
