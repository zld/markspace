package orchestrator

import (
	"os/exec"
	"testing"
)

// runGo executes a go command in dir, failing the test with combined output.
func runGo(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %v: %v\n%s", args, err, out)
	}
}
