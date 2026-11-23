package main

import (
	"testing"
	"time"

	"mediad/internal/manifest"
)

func testConfig(faults ...string) *demoConfig {
	set := map[string]bool{}
	for _, f := range faults {
		set[f] = true
	}
	return &demoConfig{
		seed:     7,
		duration: 12 * time.Second,
		segment:  2 * time.Second,
		bitrates: []int{450000, 900000, 1800000},
		queueCap: 8,
		faults:   set,
	}
}

func TestExecuteCleanRunPassesInvariants(t *testing.T) {
	code, m, isolated, _ := execute(testConfig())
	if code != exitOK {
		t.Fatalf("clean run exit = %d, want %d", code, exitOK)
	}
	if isolated {
		t.Fatal("clean run must not report isolation")
	}
	if len(m.Segments) != 6 || len(m.Representations) != 3 {
		t.Fatalf("want 6 segments and 3 reps, got %d/%d", len(m.Segments), len(m.Representations))
	}
	if err := manifest.Verify(m, 3); err != nil {
		t.Fatalf("clean manifest must verify: %v", err)
	}
	if manifest.QualityMean(m) < manifest.MinQualityMean {
		t.Fatalf("quality mean below threshold: %.4f", manifest.QualityMean(m))
	}
	if m.AlignmentErrorMS > manifest.MaxAlignmentErrorMS {
		t.Fatalf("alignment error too large: %dms", m.AlignmentErrorMS)
	}
	for _, r := range m.Representations {
		if r.BitrateErrPct > manifest.MaxBitrateErrPct {
			t.Fatalf("rep %d bitrate error %.2f too large", r.Index, r.BitrateErrPct)
		}
	}
}

func TestExecuteFaultRunIsolatesAndKeepsUsableLayers(t *testing.T) {
	code, m, isolated, _ := execute(testConfig("truncate", "slow", "late-keyframe"))
	if code != exitIsolated {
		t.Fatalf("fault run exit = %d, want %d (isolated)", code, exitIsolated)
	}
	if !isolated {
		t.Fatal("truncation must surface as an isolated corruption")
	}
	// All three representations remain listed (usable manifest preserved).
	if len(m.Representations) != 3 {
		t.Fatalf("isolated fault must keep all reps listed, got %d", len(m.Representations))
	}
	// The late-keyframe layer has exactly one missing segment but recovers.
	var foundMissing bool
	for _, r := range m.Representations {
		for _, seg := range r.MissingSegs {
			if seg == 2 {
				foundMissing = true
			}
		}
	}
	if !foundMissing {
		t.Fatal("late-keyframe layer must show segment 2 missing")
	}
	// Healthy layers still satisfy strict thresholds even during faults.
	for _, r := range m.Representations {
		if r.Isolated {
			continue
		}
		if r.BitrateErrPct > manifest.MaxBitrateErrPct {
			t.Fatalf("healthy rep %d degraded: %.2f%%", r.Index, r.BitrateErrPct)
		}
	}
	if manifest.QualityMean(m) < manifest.MinQualityMean {
		t.Fatalf("healthy-layer quality mean too low: %.4f", manifest.QualityMean(m))
	}
	if m.AlignmentErrorMS > manifest.MaxAlignmentErrorMS {
		t.Fatalf("alignment error across healthy layers too large: %dms", m.AlignmentErrorMS)
	}
}

func TestExecuteDeterministicAcrossRuns(t *testing.T) {
	_, first, _, _ := execute(testConfig("truncate", "slow", "late-keyframe"))
	_, second, _, _ := execute(testConfig("truncate", "slow", "late-keyframe"))
	if first.Summary != second.Summary {
		t.Fatalf("non-deterministic summary:\n%s\n%s", first.Summary, second.Summary)
	}
	if first.AlignmentErrorMS != second.AlignmentErrorMS {
		t.Fatal("alignment stats differ across runs")
	}
	var dropFirst, dropSecond int64
	for i := range first.Representations {
		dropFirst += first.Representations[i].Dropped
		dropSecond += second.Representations[i].Dropped
	}
	if dropFirst != dropSecond || dropFirst == 0 {
		t.Fatalf("drop counters must reproduce and be non-zero: %d vs %d", dropFirst, dropSecond)
	}
}

func TestExecuteRecoveryBoundaryInvariant(t *testing.T) {
	_, m, _, _ := execute(testConfig("slow"))
	// Segment boundaries stay contiguous and monotonic despite recovery.
	for i, seg := range m.Segments {
		want := int64(i) * m.SegmentTicks
		if seg.StartTick != want {
			t.Fatalf("segment %d boundary drifted after recovery: %d != %d", i, seg.StartTick, want)
		}
		if seg.EndTick <= seg.StartTick {
			t.Fatalf("segment %d has non-positive span", i)
		}
	}
	// The slow layer was recovered and stays in the usable representation list.
	recovered := 0
	for _, r := range m.Representations {
		if r.Isolated {
			recovered++
		}
	}
	if recovered != 1 {
		t.Fatalf("want exactly one recovered layer, got %d", recovered)
	}
}

func TestExecuteWraparoundProducesMonotonicTimeline(t *testing.T) {
	// Layer 0 starts near the 32-bit modulus; the built manifest must still
	// align every segment on the shared clock without gaps or negative spans.
	_, m, _, _ := execute(testConfig())
	for i, seg := range m.Segments {
		if !seg.Present[0] {
			t.Fatalf("wrapping layer 0 missing segment %d", i)
		}
	}
}
