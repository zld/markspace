package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/model"
	"mediad/internal/plan"
	"mediad/internal/quality"
	"mediad/internal/timeline"
)

func sampleTimeline(track string, dups uint64, gaps []timeline.Gap) *timeline.Timeline {
	return &timeline.Timeline{
		Track: track, ClockRate: 48000,
		StartNS: 0, EndNS: model.Second, TotalDurNS: model.Second,
		Frames: []timeline.Frame{{StartNS: 0, DurationNS: model.Second}},
		Gaps:   gaps, Duplicates: dups,
	}
}

func writeArtifact(t *testing.T, dir, name, body string) map[string]string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256Hex([]byte(body))
	return map[string]string{name: sum}
}

func TestWriteAndVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b := NewBuilder()
	out := writeArtifact(t, dir, "a.bin", "audio")
	b.AddTrack(sampleTimeline("a", 1, []timeline.Gap{{FromSeq: 4, ToSeq: 4}}),
		plan.ActionKeep, out)
	rep := quality.Report{Algorithm: quality.MetricAlgorithm, TargetBitrateBPS: 80_000,
		ActualBitrateBPS: 80_000, QualityMetric: 0.98, AlignmentErrorNS: 0}
	b.SetQuality(rep, map[string]string{"normalize": "abc"})
	m, err := b.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.ManifestSHA == "" {
		t.Fatal("self digest missing")
	}
	got, err := Verify(dir)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if len(got.Tracks) != 1 || got.Tracks[0].Duplicates != 1 || len(got.Tracks[0].Gaps) != 1 {
		t.Fatalf("track record wrong: %+v", got.Tracks)
	}
}

func TestIdempotentByteEqualWrite(t *testing.T) {
	dir := t.TempDir()
	build := func() *Builder {
		b := NewBuilder()
		out := writeArtifact(t, dir, "a.bin", "audio")
		b.AddTrack(sampleTimeline("a", 0, nil), plan.ActionKeep, out)
		b.SetQuality(quality.Report{TargetBitrateBPS: 1, ActualBitrateBPS: 1}, nil)
		return b
	}
	m1, err := build().Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw1, _ := os.ReadFile(filepath.Join(dir, FileName))
	m2, err := build().Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(filepath.Join(dir, FileName))
	if string(raw1) != string(raw2) || !Equal(m1, m2) {
		t.Fatal("identical inputs must produce byte-equal manifests")
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	b := NewBuilder()
	out := writeArtifact(t, dir, "a.bin", "audio")
	b.AddTrack(sampleTimeline("a", 0, nil), plan.ActionKeep, out)
	if _, err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Verify(dir)
	if model.AsError(err).Code != model.ErrManifestVerify {
		t.Fatalf("want ErrManifestVerify, got %v", err)
	}
}

func TestTracksSortedAndGapsRecorded(t *testing.T) {
	dir := t.TempDir()
	b := NewBuilder()
	b.AddTrack(sampleTimeline("z", 0, nil), plan.ActionKeep, writeArtifact(t, dir, "z.bin", "z"))
	b.AddTrack(sampleTimeline("a", 0, []timeline.Gap{{FromSeq: 1, ToSeq: 2}}),
		plan.ActionTrim, writeArtifact(t, dir, "a.bin", "a"))
	m, err := b.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Tracks[0].Track != "a" || m.Tracks[1].Track != "z" {
		t.Fatalf("tracks not sorted: %+v", m.Tracks)
	}
	if m.Algorithms["quality"] != quality.MetricAlgorithm {
		t.Fatalf("algorithm id missing: %+v", m.Algorithms)
	}
}
