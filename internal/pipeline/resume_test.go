package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"mediarun/internal/gen"
)

// TestResumeReusesVerifiedSegments proves the resume path does not rewrite an
// unchanged segment: the file's change time is left alone.
func TestResumeReusesVerifiedSegments(t *testing.T) {
	dir := t.TempDir()
	if _, err := gen.Generate(gen.Options{Dir: dir, SegmentCount: 3, FramesPerSeg: 5}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.SegmentCount = 3
	run := func(resume bool) *Runner {
		r, err := New(dir, Inputs{
			FramesPath: filepath.Join(dir, "frames.mfr"),
			AudioPath:  filepath.Join(dir, "audio.rtp"),
		}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Resume(resume); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Run(); err != nil {
			t.Fatal(err)
		}
		return r
	}
	run(false)
	seg := filepath.Join(dir, SegmentsDir, "segment-01.mp4")
	before, err := os.Stat(seg)
	if err != nil {
		t.Fatal(err)
	}
	r := run(true)
	after, err := os.Stat(seg)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("resumed run rewrote an unchanged segment")
	}
	if r.ResumedStage() != "complete" {
		t.Fatalf("resume stage = %s", r.ResumedStage())
	}
}

// TestResumeRebuildsTamperedSegment proves a cached segment is not trusted
// when its bytes no longer match what the checkpoint recorded.
func TestResumeRebuildsTamperedSegment(t *testing.T) {
	dir := t.TempDir()
	if _, err := gen.Generate(gen.Options{Dir: dir, SegmentCount: 2, FramesPerSeg: 5}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.SegmentCount = 2
	newRunner := func() *Runner {
		r, err := New(dir, Inputs{
			FramesPath: filepath.Join(dir, "frames.mfr"),
			AudioPath:  filepath.Join(dir, "audio.rtp"),
		}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Resume(true); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := newRunner()
	m1, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(dir, SegmentsDir, "segment-00.mp4")
	original, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	want := m1.Segments[0].SHA256
	// Corrupt a byte inside the first sample payload.
	tampered := append([]byte(nil), original...)
	tampered[len(tampered)/2] ^= 0xFF
	if err := os.WriteFile(seg, tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	r2 := newRunner()
	m2, err := r2.Run()
	if err != nil {
		t.Fatalf("resume over tampered segment: %v", err)
	}
	if m2.Segments[0].SHA256 != want {
		t.Fatal("tampered segment was not rebuilt to the original bytes")
	}
	repaired, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	if len(repaired) != len(original) {
		t.Fatalf("rebuilt segment size %d != %d", len(repaired), len(original))
	}
	vr, err := Verify(dir, m2)
	if err != nil {
		t.Fatal(err)
	}
	if vr.RoundtripRatio() != 100 || !vr.VerifiedSegments() {
		t.Fatalf("verify = %+v", vr)
	}
}

// TestResumeInvalidatedByInputChange ensures a changed fixture is not resumed
// from a checkpoint built for the old bytes.
func TestResumeInvalidatedByInputChange(t *testing.T) {
	dir := t.TempDir()
	if _, err := gen.Generate(gen.Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	newRunner := func() *Runner {
		r, err := New(dir, Inputs{
			FramesPath: filepath.Join(dir, "frames.mfr"),
			AudioPath:  filepath.Join(dir, "audio.rtp"),
		}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Resume(true); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if _, err := newRunner().Run(); err != nil {
		t.Fatal(err)
	}
	// Regenerate with different content.
	if _, err := gen.Generate(gen.Options{Dir: dir, Inject: []string{"dupe"}}); err != nil {
		t.Fatal(err)
	}
	r := newRunner()
	if r.ResumedStage() != "parse" {
		t.Fatalf("input change resumed at %q, want parse", r.ResumedStage())
	}
	m, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range m.Faults {
		if f.Kind == "duplicate_packet" {
			found = true
		}
	}
	if !found {
		t.Fatalf("new input faults missing: %+v", m.Faults)
	}
}
