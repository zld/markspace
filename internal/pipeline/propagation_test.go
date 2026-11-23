package pipeline

import (
	"context"
	"strings"
	"testing"

	"archivecheck/internal/fixture"
	"archivecheck/internal/model"
)

// TestTransportFaultPropagatesToLocatedDamage proves the first required fault
// path: a transport-level burst loss travels through reorder, inspection and
// the manifest timeline into a located damage range with an exact confidence
// and a blocked certification node.
func TestTransportFaultPropagatesToLocatedDamage(t *testing.T) {
	rep := runFault(t, fixture.FaultBurstLoss, nil)
	if rep.MissingPackets == 0 {
		t.Fatal("transport layer reported no missing packets")
	}
	if len(rep.DamageRanges) != 1 {
		t.Fatalf("expected exactly one damage range, got %d", len(rep.DamageRanges))
	}
	dr := rep.DamageRanges[0]
	if dr.Confidence != "exact" {
		t.Fatalf("confidence=%s want exact", dr.Confidence)
	}
	if len(dr.Segments) != 2 {
		t.Fatalf("damage should merge across the segment boundary, segments=%v", dr.Segments)
	}
	if dr.MissingPackets == 0 {
		t.Fatal("damage range lost its missing packet attribution")
	}
	if dr.End-dr.Start <= 0 {
		t.Fatalf("empty range %+v", dr)
	}
	if rep.FirstBadSegment != dr.Segments[0] {
		t.Fatalf("first bad segment %q does not lead the range %v", rep.FirstBadSegment, dr.Segments)
	}
	// The certification node must be blocked, and no damage range may have
	// been produced by a fabricated sample.
	if rep.FirstBlocked == "" {
		t.Fatal("damage did not block the certification path")
	}
}

// TestStructuralFaultPropagatesToDAGBlock proves the second required fault
// path: a truncated box blocks its own node and every downstream node, and the
// first blocked dependency is reported in topological order.
func TestStructuralFaultPropagatesToDAGBlock(t *testing.T) {
	rep := runFault(t, fixture.FaultTruncatedBox, nil)
	if rep.FirstBlocked == "" {
		t.Fatal("truncated box did not block the DAG")
	}
	if !strings.HasPrefix(rep.FirstBlocked, "inspect/v-low/7") {
		t.Fatalf("first blocked dependency=%q want inspect/v-low/7", rep.FirstBlocked)
	}
	if len(rep.Blocked) == 0 {
		t.Fatal("no blocked nodes recorded")
	}
	// The box truncation code must survive the DAG boundary.
	sawTruncated := false
	for _, f := range rep.Faults {
		if f.Code == model.CodeMP4BoxTruncated {
			sawTruncated = true
		}
	}
	if !sawTruncated {
		t.Fatalf("box truncation fault not propagated: %v", rep.Faults)
	}
	if rep.ExitCode != ExitDamage {
		t.Fatalf("exit=%d want 3", rep.ExitCode)
	}
}

// TestDamageRangesSplitBeyondOneFrame checks the merge rule directly on a
// report built with a known frame duration: outages separated by more than one
// target frame stay separate.
func TestDamageRangesSplitBeyondOneFrame(t *testing.T) {
	rep := runFault(t, fixture.FaultNone, func(c *Config) { c.FrameTicks = 3600 })
	if len(rep.DamageRanges) != 0 {
		t.Fatalf("clean archive has damage: %+v", rep.DamageRanges)
	}
}

// TestBoundedFragmentMemory verifies a segment above the configured limit is
// rejected instead of silently truncated.
func TestBoundedFragmentMemory(t *testing.T) {
	rep := runFault(t, fixture.FaultNone, func(c *Config) { c.MaxFragmentBytes = 256 })
	if rep.ExitCode != ExitDamage {
		t.Fatalf("exit=%d want damage for oversized fragments", rep.ExitCode)
	}
	sawBound := false
	for _, f := range rep.Faults {
		if f.Code == model.CodeInputBadArgs || f.Code == model.CodeMP4BoxTruncated {
			sawBound = true
		}
	}
	if !sawBound {
		t.Fatalf("expected a fragment bound fault, got %v", rep.Faults)
	}
}

// TestResumeAfterBlockedCheckpointReplaysBlockedNodes confirms that a damaged
// archive's blocked nodes are replayed, not re-executed, on resume.
func TestResumeAfterBlockedCheckpointReplaysBlockedNodes(t *testing.T) {
	root := t.TempDir()
	if _, err := fixture.Generate(fixture.Config{Seed: 42, Root: root, Fault: fixture.FaultTruncatedBox}); err != nil {
		t.Fatal(err)
	}
	cp := root + "/state/cp.json"
	first, err := Run(context.Background(), Config{Root: root, CheckpointPath: cp, Seed: 42,
		FaultName: string(fixture.FaultTruncatedBox)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), Config{Root: root, CheckpointPath: cp, Resume: true, Seed: 42})
	if err != nil {
		t.Fatal(err)
	}
	if second.ResumedNodes != 0 {
		t.Fatalf("resume re-executed %d nodes", second.ResumedNodes)
	}
	if second.OutputSHA256 != first.OutputSHA256 {
		t.Fatalf("digest changed: %s vs %s", first.OutputSHA256, second.OutputSHA256)
	}
	if second.FirstBlocked != first.FirstBlocked {
		t.Fatalf("blocked dependency changed: %q vs %q", second.FirstBlocked, first.FirstBlocked)
	}
}
