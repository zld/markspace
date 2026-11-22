package manifest_test

import (
	"encoding/json"
	"testing"

	"mediacheck/internal/align"
	"mediacheck/internal/manifest"
	"mediacheck/internal/timeline"
)

func boundariesFixture() []align.Boundary {
	return []align.Boundary{
		{Index: 0, T0: 0, FrameIndex: map[string]int{"r0": 0, "r1": 0}},
		{Index: 1, T0: 1_000_000_000, FrameIndex: map[string]int{"r0": 25, "r1": 25}},
		{Index: 2, T0: 2_000_000_000, FrameIndex: map[string]int{"r0": 50, "r1": 50}},
		{Index: 3, T0: 3_000_000_000, FrameIndex: map[string]int{"r0": 75, "r1": 75}},
	}
}

func testConfig() manifest.Config {
	return manifest.Config{
		TargetSegmentNS:   1_000_000_000,
		MaxSkewNS:         20_000_000,
		DriftLimitPPM:     250,
		RepresentationIDs: []string{"r0", "r1"},
	}
}

func TestDeterministicManifestAndChecksum(t *testing.T) {
	b, err := manifest.NewBuilder(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	ends := map[string]int64{"r0": 3_960_000_000, "r1": 3_960_000_000}
	m1, err := b.Build("abc123", boundariesFixture(), ends)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := b.Build("abc123", boundariesFixture(), ends)
	if err != nil {
		t.Fatal(err)
	}
	if m1.OutputSHA256 != m2.OutputSHA256 {
		t.Fatal("checksum not deterministic")
	}
	if len(m1.OutputSHA256) != 64 {
		t.Fatalf("checksum length=%d want 64 hex chars", len(m1.OutputSHA256))
	}
	raw1, _ := m1.Serialize()
	raw2, _ := m2.Serialize()
	if string(raw1) != string(raw2) {
		t.Fatal("serialised manifests differ for identical input")
	}
}

func TestInputDigestChangesManifest(t *testing.T) {
	b, _ := manifest.NewBuilder(testConfig())
	ends := map[string]int64{"r0": 3_960_000_000, "r1": 3_960_000_000}
	m1, _ := b.Build("input-a", boundariesFixture(), ends)
	m2, _ := b.Build("input-b", boundariesFixture(), ends)
	if m1.InputSHA256 == m2.InputSHA256 || m1.OutputSHA256 == m2.OutputSHA256 {
		t.Fatal("different inputs must yield different input digest and checksum")
	}
}

func TestSegmentContinuityAndBoundaryConsistency(t *testing.T) {
	b, _ := manifest.NewBuilder(testConfig())
	ends := map[string]int64{"r0": 3_960_000_000, "r1": 3_960_000_000}
	m, err := b.Build("digest", boundariesFixture(), ends)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 4 {
		t.Fatalf("segments=%d want 4", len(m.Segments))
	}
	// Index contiguous, durations positive, frame indices cover both layers.
	for i, s := range m.Segments {
		if s.Index != i {
			t.Fatalf("segment index gap at %d: %d", i, s.Index)
		}
		if s.DurationNS <= 0 {
			t.Fatalf("segment %d non-positive duration %d", i, s.DurationNS)
		}
		if len(s.RepFrameIdx) != 2 {
			t.Fatalf("segment %d missing representation frame indices", i)
		}
	}
	// Each representation summary reports the same segment count.
	for _, r := range m.Representations {
		if r.Segments != 4 {
			t.Fatalf("rep %s segments=%d want 4", r.ID, r.Segments)
		}
	}
}

func TestManifestIsValidJSON(t *testing.T) {
	b, _ := manifest.NewBuilder(testConfig())
	ends := map[string]int64{"r0": 3_960_000_000, "r1": 3_960_000_000}
	m, err := b.Build("digest", boundariesFixture(), ends)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip manifest.Manifest
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatalf("manifest not valid JSON: %v", err)
	}
	if roundTrip.OutputSHA256 != m.OutputSHA256 {
		t.Fatal("checksum lost in JSON round trip")
	}
}

func TestGoMp4StructuralValidation(t *testing.T) {
	if err := manifest.ValidateStructural(); err != nil {
		t.Fatalf("go-mp4 ftyp structural check failed: %v", err)
	}
}

func TestRejectsTooFewBoundaries(t *testing.T) {
	b, _ := manifest.NewBuilder(testConfig())
	one := []align.Boundary{{Index: 0, T0: 0, FrameIndex: map[string]int{"r0": 0}}}
	if _, err := b.Build("x", one, map[string]int64{"r0": 1}); err == nil {
		t.Fatal("single boundary must be rejected")
	}
}

// keep timeline import meaningful for frame-based future extensions.
var _ = timeline.KindVideo
