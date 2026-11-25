package pipeline_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/pipeline"
)

func TestNormalDemoStableAndIdempotent(t *testing.T) {
	dir1 := t.TempDir()
	out1, err := pipeline.Run(pipeline.Config{Seed: 17, WorkDir: dir1})
	if err != nil {
		t.Fatal(err)
	}
	if out1.ExitCode != pipeline.ExitOK {
		t.Fatalf("exit=%d", out1.ExitCode)
	}
	m := out1.Manifest
	if m.Summary.Tracks != 3 || m.Summary.Frames != 12 || m.Summary.DuplicateGroups != 1 {
		t.Fatalf("unexpected summary: %+v", m.Summary)
	}
	if m.Summary.AlignmentErrorMS > 1 {
		t.Fatalf("alignment error too large: %dms", m.Summary.AlignmentErrorMS)
	}
	if m.Summary.FrameSHA256 == "" || m.Summary.SegmentSHA256 == "" || out1.ManifestSHA256 == "" {
		t.Fatal("missing hashes")
	}

	// A second run in another directory must produce identical manifest bytes:
	// no absolute paths or wall-clock values are archived.
	dir2 := t.TempDir()
	out2, err := pipeline.Run(pipeline.Config{Seed: 17, WorkDir: dir2})
	if err != nil {
		t.Fatal(err)
	}
	if out1.ManifestSHA256 != out2.ManifestSHA256 {
		t.Fatalf("manifest differs across work dirs: %s != %s", out1.ManifestSHA256, out2.ManifestSHA256)
	}

	// Retrying in place must merge with the existing manifest without adding
	// another job record or changing bytes.
	retry, err := pipeline.Run(pipeline.Config{Seed: 17, WorkDir: dir1})
	if err != nil {
		t.Fatal(err)
	}
	if retry.ManifestSHA256 != out1.ManifestSHA256 {
		t.Fatalf("retry manifest changed: %s != %s", retry.ManifestSHA256, out1.ManifestSHA256)
	}
	jobs, err := os.ReadDir(filepath.Join(dir1, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("job files=%d, want 1", len(jobs))
	}
}

func TestInterruptTwiceAndResumeMatchesCleanRun(t *testing.T) {
	cleanDir := t.TempDir()
	clean, err := pipeline.Run(pipeline.Config{Seed: 17, WorkDir: cleanDir})
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	for i := 0; i < 2; i++ {
		stop, err := pipeline.Run(pipeline.Config{Seed: 17, WorkDir: work, InterruptAfter: "scan"})
		if err != nil {
			t.Fatal(err)
		}
		if !stop.Interrupted || stop.ExitCode != pipeline.ExitOK {
			t.Fatalf("interrupt outcome=%+v", stop)
		}
	}
	jobs, _ := os.ReadDir(filepath.Join(work, "jobs"))
	if len(jobs) != 1 {
		t.Fatalf("interrupt job files=%d", len(jobs))
	}

	resumed, err := pipeline.Run(pipeline.Config{Seed: 17, WorkDir: work, Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Resumed || resumed.ResumeFrom != "scan" {
		t.Fatalf("resume provenance=%+v from=%q", resumed.Resumed, resumed.ResumeFrom)
	}
	if resumed.ManifestSHA256 != clean.ManifestSHA256 {
		t.Fatalf("resume hash %s != clean %s", resumed.ManifestSHA256, clean.ManifestSHA256)
	}
}

func TestChangedFingerprintRejectsCheckpoint(t *testing.T) {
	work := t.TempDir()
	if _, err := pipeline.Run(pipeline.Config{Seed: 17, WorkDir: work, InterruptAfter: "scan"}); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(work, "input", "media-001.mp4")
	f, err := os.OpenFile(input, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := pipeline.Run(pipeline.Config{Seed: 17, WorkDir: work, Resume: true})
	if err == nil {
		t.Fatal("expected fingerprint mismatch")
	}
	if out == nil || out.ExitCode != pipeline.ExitState {
		t.Fatalf("outcome=%+v err=%v", out, err)
	}
	if !fault.IsCode(err, fault.CodeCheckpointMismatch) {
		t.Fatalf("err=%v, want checkpoint mismatch", err)
	}
}

func TestCorruptionModesReportStructuredDamage(t *testing.T) {
	cases := map[string]struct {
		wantKind string
	}{
		"truncated-box":        {wantKind: "truncated-box"},
		"missing-keyframe":     {wantKind: "missing-keyframe"},
		"timestamp-regression": {wantKind: "timestamp-regression"},
	}
	for corrupt, tc := range cases {
		t.Run(corrupt, func(t *testing.T) {
			out, err := pipeline.Run(pipeline.Config{Seed: 17, Corrupt: corrupt, WorkDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if out.ExitCode != pipeline.ExitDamage {
				t.Fatalf("exit=%d, want %d", out.ExitCode, pipeline.ExitDamage)
			}
			d := out.Damage
			if d == nil || d.DamageCount < 1 || d.FirstBadOffset <= 0 {
				t.Fatalf("bad damage report: %+v", d)
			}
			if d.TrackID != 1 || d.StartMS >= d.EndMS {
				t.Fatalf("missing track/time range: %+v", d)
			}
			if len(d.Segments) == 0 || d.Segments[0].Kind != tc.wantKind {
				t.Fatalf("kind=%q, want %q", d.Segments[0].Kind, tc.wantKind)
			}
			if out.Manifest == nil || out.Manifest.Diagnostics == nil {
				t.Fatal("diagnostic was not archived")
			}
			if len(out.Manifest.Issues) == 0 {
				t.Fatal("fault sources were not retained in manifest")
			}
		})
	}
}

func TestUsageErrorWithoutWork(t *testing.T) {
	out, err := pipeline.Run(pipeline.Config{})
	if err == nil {
		t.Fatal("expected usage error")
	}
	var fe *fault.Error
	if !errors.As(err, &fe) {
		t.Fatalf("expected fault.Error, got %T", err)
	}
	if out == nil || out.ExitCode != pipeline.ExitUsage {
		t.Fatalf("out=%+v", out)
	}
}
