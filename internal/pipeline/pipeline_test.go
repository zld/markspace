package pipeline

import (
	"testing"

	"archivist/internal/container"
)

func baseConfig(t *testing.T, corruption string) Config {
	t.Helper()
	return Config{
		Seed: 17, Bitrates: []uint32{800000, 1600000}, SegmentMS: 2000,
		Corruption: corruption, Retries: 2, OutputDir: t.TempDir(),
	}
}

func TestHealthyPipelineQuantitative(t *testing.T) {
	m, err := New(baseConfig(t, container.CorruptNone)).Run()
	if err != nil {
		t.Fatalf("healthy run should succeed: %v", err)
	}
	if m.ExitCode != 0 || m.RoundtripRate != 1.0 || m.KeyframeHitRate != 1.0 {
		t.Fatalf("baseline metrics wrong: %+v", m)
	}
	if m.Segments != 6 || m.AlignedBoundaries != "6/6" || m.SegmentHashMatch != "6/6" {
		t.Fatalf("segment metrics wrong: %+v", m)
	}
	if m.RecoveredIndexes != 0 || !m.ManifestWritten || !m.RerunDigestEqual {
		t.Fatalf("publication/rerun metrics wrong: %+v", m)
	}
}

func TestTruncatedBoxRecoversAndPublishes(t *testing.T) {
	m, err := New(baseConfig(t, container.CorruptTruncatedBox)).Run()
	if err != nil {
		t.Fatalf("truncated input is recoverable: %v", err)
	}
	if m.RecoveredIndexes != 1 || m.ExitCode != 0 || !m.ManifestWritten {
		t.Fatalf("recovery metrics wrong: %+v", m)
	}
	if m.AlignedBoundaries != "6/6" || m.SegmentHashMatch != "6/6" {
		t.Fatalf("recovered asset should still fully align: %+v", m)
	}
}

func TestMissingKeyframeRebuilt(t *testing.T) {
	m, err := New(baseConfig(t, container.CorruptMissingKeyframe)).Run()
	if err != nil {
		t.Fatalf("missing stss should rebuild from markers: %v", err)
	}
	if m.RecoveredIndexes != 2 { // two video tracks rebuilt
		t.Fatalf("rebuilt count = %d, want 2", m.RecoveredIndexes)
	}
	if !m.ManifestWritten || m.AlignedBoundaries != "6/6" {
		t.Fatalf("rebuilt index must allow publication: %+v", m)
	}
}

func TestKeyframeDriftBlocksManifest(t *testing.T) {
	m, err := New(baseConfig(t, container.CorruptKeyframeDrift)).Run()
	if err == nil {
		t.Fatal("drift must produce a blocking error")
	}
	if m.ExitCode != 2 || m.ManifestWritten {
		t.Fatalf("blocked publication wrong: %+v", m)
	}
	if m.AlignedBoundaries != "0/6" || m.BlockedBy != "keyframe-index:video-high" {
		t.Fatalf("drift reporting wrong: %+v", m)
	}
}

func TestCorruptSampleTableRejected(t *testing.T) {
	m, err := New(baseConfig(t, container.CorruptSampleTable)).Run()
	if err == nil {
		t.Fatal("corrupt table must be rejected")
	}
	if m.ExitCode != 2 || m.ManifestWritten || m.RecoveredIndexes != 0 {
		t.Fatalf("rejection metrics wrong: %+v", m)
	}
	if m.BlockedBy == "none" {
		t.Fatal("blocked_by must name the failing sample table")
	}
}

func TestAudioStartDelayPropagates(t *testing.T) {
	m, err := New(baseConfig(t, container.CorruptAudioOffset)).Run()
	if err != nil {
		t.Fatalf("audio offset is recoverable: %v", err)
	}
	if !m.ManifestWritten || m.ExitCode != 0 {
		t.Fatalf("audio delay should not block publication: %+v", m)
	}
	foundDelay := false
	for _, d := range m.Decisions {
		if d.Scope == "audio:3" {
			foundDelay = true
		}
	}
	if !foundDelay {
		t.Fatal("audio normalization decision must be recorded")
	}
}

func TestRerunDigestDeterministic(t *testing.T) {
	cfg := baseConfig(t, container.CorruptNone)
	m1, err := New(cfg).Run()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := New(cfg).Run()
	if err != nil {
		t.Fatal(err)
	}
	if m1.Digest == "" || m1.Digest != m2.Digest {
		t.Fatalf("rerun digest mismatch: %q vs %q", m1.Digest, m2.Digest)
	}
	if !m1.RerunDigestEqual || !m2.RerunDigestEqual {
		t.Fatal("both runs must report stable rerun digest")
	}
}
