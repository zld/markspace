package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI invokes the command and captures combined output via temporary files,
// since run writes to *os.File handles rather than io.Writer.
func runCLI(t *testing.T, args ...string) (code int, out string) {
	t.Helper()
	dir := t.TempDir()
	outFile, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()
	errFile, err := os.Create(filepath.Join(dir, "err"))
	if err != nil {
		t.Fatal(err)
	}
	defer errFile.Close()

	code = run(args, outFile, errFile)
	o, _ := os.ReadFile(filepath.Join(dir, "out"))
	e, _ := os.ReadFile(filepath.Join(dir, "err"))
	return code, string(o) + string(e)
}

func TestCLINoArgs(t *testing.T) {
	if code, out := runCLI(t); code != exitHard || !strings.Contains(out, "Usage") {
		t.Fatalf("code = %d, out = %q", code, out)
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	if code, _ := runCLI(t, "bogus"); code != exitHard {
		t.Fatalf("code = %d", code)
	}
}

func TestCLICases(t *testing.T) {
	code, out := runCLI(t, "cases")
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out, "corrupt-hash") || !strings.Contains(out, "cache-boundary") {
		t.Fatalf("case list incomplete: %q", out)
	}
}

func TestCLIUnknownCase(t *testing.T) {
	if code, _ := runCLI(t, "demo", "--case", "nope"); code != exitHard {
		t.Fatalf("code = %d", code)
	}
}

func TestCLIRejectsBadFlags(t *testing.T) {
	if code, _ := runCLI(t, "demo", "--seed", "-1"); code != exitHard {
		t.Fatalf("negative seed accepted")
	}
	if code, _ := runCLI(t, "demo", "--workers", "0"); code != exitHard {
		t.Fatalf("zero workers accepted")
	}
}

func TestCLINormalSucceeds(t *testing.T) {
	code, out := runCLI(t, "demo", "--seed", "7", "--workers", "2",
		"--out", t.TempDir())
	if code != exitOK {
		t.Fatalf("code = %d, out = %q", code, out)
	}
	for _, want := range []string{"run hash:", "peak cache bytes:", "max align error:", "exit code:         0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q", want)
		}
	}
}

func TestCLICorruptHashExitsOne(t *testing.T) {
	code, out := runCLI(t, "demo", "--case", "corrupt-hash", "--out", t.TempDir())
	if code != exitHard {
		t.Fatalf("code = %d, out = %q", code, out)
	}
	if !strings.Contains(out, "hash_mismatch") || !strings.Contains(out, "track 2 frame 2") {
		t.Fatalf("first error not reported: %q", out)
	}
}

func TestCLIBudgetConflictExitsOne(t *testing.T) {
	code, out := runCLI(t, "demo", "--case", "budget-conflict", "--out", t.TempDir())
	if code != exitHard {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out, "NO FEASIBLE CANDIDATE") || !strings.Contains(out, "candidate summary") {
		t.Fatalf("conflict not listed: %q", out)
	}
}
