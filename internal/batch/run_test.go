package batch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"subx/internal/format"
	"subx/internal/timeline"
)

func testOptions() Options {
	fps, _ := timeline.ParseFPS("24000/1001")
	return Options{To: format.VTT, FPS: fps, Overlap: timeline.Keep, Jobs: 4}
}

const twoCues = "1\n00:00:01,000 --> 00:00:03,000\n你好\n\n2\n00:00:04,000 --> 00:00:05,000\nworld\n"

func TestConvertOne(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "a.srt")
	out := filepath.Join(dir, "a.vtt")
	if err := os.WriteFile(in, []byte(twoCues), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := ConvertOne(Job{in, out}, testOptions())
	if rep.Code != 0 || rep.Status != "ok" {
		t.Fatalf("rep: %+v", rep)
	}
	if rep.InputCount != 2 || rep.OutputCount != 2 || rep.DroppedCount != 0 {
		t.Fatalf("counts: %+v", rep)
	}
	if rep.MaxAbsDeltaMs > 42 {
		t.Fatalf("max delta %d", rep.MaxAbsDeltaMs)
	}
	if len(rep.Cues) != 2 || rep.Cues[0].Status != "ok" {
		t.Fatalf("cues: %+v", rep.Cues)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output missing: %v", err)
	}
}

func TestConvertOneRejectsSamePathAndBadInput(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "a.srt")
	if err := os.WriteFile(in, []byte(twoCues), 0o644); err != nil {
		t.Fatal(err)
	}
	if rep := ConvertOne(Job{in, in}, testOptions()); rep.Code != CodeIO {
		t.Fatalf("same path: code=%d", rep.Code)
	}
	bad := filepath.Join(dir, "bad.srt")
	os.WriteFile(bad, []byte("1\ngarbage\n"), 0o644)
	rep := ConvertOne(Job{bad, filepath.Join(dir, "bad.vtt")}, testOptions())
	if rep.Code != CodeFormat {
		t.Fatalf("bad format: code=%d", rep.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.vtt")); !os.IsNotExist(err) {
		t.Fatal("failed conversion left output behind")
	}
}

func TestPlanDetectsConflicts(t *testing.T) {
	dir := t.TempDir()
	inDir := filepath.Join(dir, "in")
	os.MkdirAll(inDir, 0o755)
	os.WriteFile(filepath.Join(inDir, "a.srt"), []byte(twoCues), 0o644)
	os.WriteFile(filepath.Join(inDir, "a.vtt"), []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nx\n"), 0o644)
	os.WriteFile(filepath.Join(inDir, "notes.txt"), []byte("ignore me"), 0o644)
	if _, err := Plan(inDir, filepath.Join(dir, "out"), format.VTT); err == nil ||
		!strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected conflict error, got %v", err)
	}
	if err := os.Remove(filepath.Join(inDir, "a.vtt")); err != nil {
		t.Fatal(err)
	}
	jobs, err := Plan(inDir, filepath.Join(dir, "out"), format.SRT)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Output != filepath.Join(dir, "out", "a.srt") {
		t.Fatalf("jobs: %+v", jobs)
	}
}

func TestRunBatch(t *testing.T) {
	dir := t.TempDir()
	inDir := filepath.Join(dir, "in")
	os.MkdirAll(inDir, 0o755)
	os.WriteFile(filepath.Join(inDir, "b.srt"), []byte(twoCues), 0o644)
	os.WriteFile(filepath.Join(inDir, "a.srt"), []byte(twoCues), 0o644)
	jobs, err := Plan(inDir, filepath.Join(dir, "out"), format.VTT)
	if err != nil {
		t.Fatal(err)
	}
	reps := Run(jobs, testOptions())
	if len(reps) != 2 || reps[0].Code != 0 || reps[1].Code != 0 {
		t.Fatalf("reps: %+v", reps)
	}
	if !strings.HasSuffix(reps[0].Input, "a.srt") {
		t.Fatalf("reports not sorted by input: %s", reps[0].Input)
	}
}
