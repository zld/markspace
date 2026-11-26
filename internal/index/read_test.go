package index_test

import (
	"context"
	"path/filepath"
	"testing"

	"example.com/archiveverify/internal/fixture"
	"example.com/archiveverify/internal/index"
)

func buildFixtureIndex(t *testing.T, caseName string) *index.Index {
	t.Helper()
	dir := t.TempDir()
	ws, err := fixture.Generate(dir, caseName)
	if err != nil {
		t.Fatal(err)
	}
	res, err := index.Build(context.Background(), ws.Manifest, index.BuildOptions{Root: dir, RTPWindow: 16})
	if err != nil {
		t.Fatal(err)
	}
	return res.Index
}

func TestReadExactSecondRangeAndAudioBoundary(t *testing.T) {
	idx := buildFixtureIndex(t, "clean")
	r, err := index.ParseRange("2s:3s")
	if err != nil {
		t.Fatal(err)
	}
	got, err := index.ReadRange(idx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Video) != 30 {
		t.Fatalf("video samples=%d want 30", len(got.Video))
	}
	if len(got.Audio) != 50 {
		t.Fatalf("audio samples=%d want 50", len(got.Audio))
	}
	if got.Video[0].StartUs != 2_000_000 || got.Video[len(got.Video)-1].EndUs != 3_000_000 {
		t.Fatalf("video endpoints %d..%d", got.Video[0].StartUs, got.Video[len(got.Video)-1].EndUs)
	}
	if got.AudioBoundary == nil || got.AudioBoundary.StartFrame != 96_000 || got.AudioBoundary.EndFrame != 144_000 {
		t.Fatalf("audio boundary=%+v", got.AudioBoundary)
	}
}

func TestReadMarksKeyframeDecodePrefix(t *testing.T) {
	idx := buildFixtureIndex(t, "clean")
	r, err := index.ParseRange("2500ms:3000ms")
	if err != nil {
		t.Fatal(err)
	}
	got, err := index.ReadRange(idx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Video) != 15 {
		t.Fatalf("in-range video=%d want 15", len(got.Video))
	}
	if len(got.DecodePrefix) != 15 {
		t.Fatalf("decode prefix=%d want keyframe-to-range 15 frames", len(got.DecodePrefix))
	}
	if !got.DecodePrefix[0].KeyFrame || !got.DecodePrefix[0].DecodePreroll {
		t.Fatalf("prefix must start at marked keyframe: %+v", got.DecodePrefix[0])
	}
}

func TestDigestStableAndAtomicResumeSkipsRewrite(t *testing.T) {
	dir := t.TempDir()
	ws, err := fixture.Generate(dir, "clean")
	if err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(dir, "out", "index.json")
	cpPath := filepath.Join(dir, "out", "checkpoint.json")
	r1, resumed1, err := index.BuildOrResume(context.Background(), ws.ManifestPath, idxPath, cpPath, index.BuildOptions{})
	if err != nil || resumed1 {
		t.Fatalf("first build resumed=%v err=%v", resumed1, err)
	}
	r2, resumed2, err := index.BuildOrResume(context.Background(), ws.ManifestPath, idxPath, cpPath, index.BuildOptions{})
	if err != nil || !resumed2 {
		t.Fatalf("second build resumed=%v err=%v", resumed2, err)
	}
	if r1.Index.Digest() != r2.Index.Digest() {
		t.Fatalf("digests differ %s vs %s", r1.Index.Digest(), r2.Index.Digest())
	}
	saved, err := index.LoadIndex(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Digest() != r1.Index.Digest() {
		t.Fatal("atomic index digest mismatch")
	}
}

func TestFaultIndexQuarantinesTruncatedAndKeepsReading(t *testing.T) {
	idx := buildFixtureIndex(t, "faults")
	if len(idx.Quarantine) != 1 {
		t.Fatalf("quarantine=%+v", idx.Quarantine)
	}
	r, err := index.ParseRange("2s:3s")
	if err != nil {
		t.Fatal(err)
	}
	got, err := index.ReadRange(idx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Video) != 30 || len(got.Audio) != 50 {
		t.Fatalf("complete fragments unreadable: v=%d a=%d", len(got.Video), len(got.Audio))
	}
}
