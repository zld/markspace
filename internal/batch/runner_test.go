package batch

import (
	"os"
	"path/filepath"
	"testing"

	"loudcheck/internal/audio"
	"loudcheck/internal/report"
)

func fp(v float64) *float64 { return &v }

func writeFixture(t *testing.T, dir, name, layout string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	err := audio.WriteFixture(p, audio.FixtureOptions{
		Layout: layout, Seconds: 1, SampleRate: 48000, TargetLoudness: -20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAnalyzeFile(t *testing.T) {
	dir := t.TempDir()
	good := writeFixture(t, dir, "good.wav", "stereo")
	rep := AnalyzeFile(good, Options{})
	if rep.Error != "" {
		t.Fatalf("unexpected error: %s", rep.Error)
	}
	if rep.IntegratedLUFS == nil || *rep.IntegratedLUFS < -21.5 || *rep.IntegratedLUFS > -18.5 {
		t.Errorf("integrated = %v, want near -20", rep.IntegratedLUFS)
	}
	if rep.TruePeakDBTP == nil || *rep.TruePeakDBTP > -10 {
		t.Errorf("true peak = %v", rep.TruePeakDBTP)
	}
	if rep.Channels != 2 || rep.SampleRate != 48000 {
		t.Errorf("info wrong: %+v", rep)
	}
}

func TestAnalyzeFileErrors(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.wav")
	if err := os.WriteFile(broken, []byte("not a wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rep := AnalyzeFile(broken, Options{}); rep.Error == "" {
		t.Error("broken file must produce an error report")
	}
	if rep := AnalyzeFile(filepath.Join(dir, "missing.wav"), Options{}); rep.Error == "" {
		t.Error("missing file must produce an error report")
	}
}

func TestRunKeepsResultsOnFailure(t *testing.T) {
	dir := t.TempDir()
	good := writeFixture(t, dir, "good.wav", "5.1")
	broken := filepath.Join(dir, "broken.wav")
	os.WriteFile(broken, []byte("junk"), 0o644)
	gates := report.Gates{IntegratedMin: fp(-24), IntegratedMax: fp(-16), TruePeakMax: fp(-1)}
	reps, sum := Run([]string{broken, good}, Options{Concurrency: 2, Gates: gates})
	if len(reps) != 2 || sum.Total != 2 || sum.Succeeded != 1 || sum.Failed != 1 {
		t.Fatalf("summary wrong: %+v", sum)
	}
	// Stable sort by path: broken.wav sorts before good.wav.
	if reps[0].Path != broken || reps[0].Error == "" {
		t.Errorf("broken file report wrong: %+v", reps[0])
	}
	if reps[1].Path != good || reps[1].Error != "" {
		t.Errorf("good file report wrong: %+v", reps[1])
	}
	if reps[1].OverallPass == nil || !*reps[1].OverallPass {
		t.Errorf("good file must pass gates: %+v", reps[1])
	}
	if sum.GateFailed != 0 {
		t.Errorf("gate_failed = %d, want 0", sum.GateFailed)
	}
}

func TestRunGateFailure(t *testing.T) {
	dir := t.TempDir()
	good := writeFixture(t, dir, "good.wav", "stereo")
	gates := report.Gates{IntegratedMax: fp(-30)} // impossibly strict
	_, sum := Run([]string{good}, Options{Concurrency: 1, Gates: gates})
	if sum.GateFailed != 1 || sum.Succeeded != 1 || sum.Failed != 0 {
		t.Errorf("summary wrong: %+v", sum)
	}
}
