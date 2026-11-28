package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/fault"
)

func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

func okSteps(log *[]string) []Step {
	mk := func(name string) Step {
		return Step{
			Name:  name,
			Apply: func(dir string) error { *log = append(*log, name); return writeFile(dir, "out", name) },
			Check: func(dir string) error {
				b, err := os.ReadFile(filepath.Join(dir, "out"))
				if err != nil || string(b) != name {
					return errors.New("bad staged output")
				}
				return nil
			},
		}
	}
	return []Step{mk("migrate-1"), mk("migrate-2")}
}

func TestRunAllStages(t *testing.T) {
	var log []string
	staging := filepath.Join(t.TempDir(), "staging")
	if err := Run(okSteps(&log), staging, ""); err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0] != "migrate-1" || log[1] != "migrate-2" {
		t.Fatalf("log %v", log)
	}
}

func TestInterruptAfterStage(t *testing.T) {
	var log []string
	staging := filepath.Join(t.TempDir(), "staging")
	err := Run(okSteps(&log), staging, "migrate-1")
	if !fault.HasCode(err, fault.ErrInterrupted) {
		t.Fatalf("got %v", err)
	}
	if len(log) != 1 {
		t.Fatalf("stage 2 must not run, log %v", log)
	}
	// staged output of stage 1 is preserved for inspection
	if _, err := os.Stat(filepath.Join(staging, "migrate-1", "out")); err != nil {
		t.Fatal("staged output missing")
	}
}

func TestFailingStageStopsPipeline(t *testing.T) {
	var log []string
	steps := okSteps(&log)
	steps[1].Check = func(dir string) error { return errors.New("stage 2 invalid") }
	err := Run(steps, filepath.Join(t.TempDir(), "staging"), "")
	if err == nil || fault.HasCode(err, fault.ErrInterrupted) {
		t.Fatalf("got %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("log %v", log)
	}
}
