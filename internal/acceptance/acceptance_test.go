package acceptance

import (
	"io"
	"path/filepath"
	"testing"
)

func TestAllScenarioPasses(t *testing.T) {
	code := Run(Options{
		Scenario:         "all",
		Root:             filepath.Join(t.TempDir(), "acceptance"),
		CheckpointFrames: 4,
		AfterBlock:       1,
	}, io.Discard)
	if code != ExitOK {
		t.Fatalf("all scenario exit code=%d want 0", code)
	}
}

func TestUnknownScenarioUsageExit(t *testing.T) {
	code := Run(Options{Scenario: "nope", Root: t.TempDir()}, io.Discard)
	if code != ExitUsage {
		t.Fatalf("code=%d want %d", code, ExitUsage)
	}
}
