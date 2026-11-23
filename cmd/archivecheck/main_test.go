package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archivecheck/internal/pipeline"
)

// runCLI invokes the command in-process and returns its exit code.
func runCLI(t *testing.T, args ...string) int {
	t.Helper()
	return run(args)
}

// captureStdout runs fn with stdout redirected to a pipe and returns what it
// printed. The pipe is drained concurrently so large reports cannot deadlock.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestDemoCleanExitCode(t *testing.T) {
	root := t.TempDir()
	if code := runCLI(t, "gen", "--seed", "42", "--fault", "none", "--root", root); code != 0 {
		t.Fatalf("gen exit=%d", code)
	}
	if code := runCLI(t, "check", "--root", root); code != pipeline.ExitOK {
		t.Fatalf("clean check exit=%d want 0", code)
	}
}

func TestDemoBurstLossExitCodeAndKeys(t *testing.T) {
	root := t.TempDir()
	if code := runCLI(t, "gen", "--seed", "42", "--fault", "burst-loss", "--root", root); code != 0 {
		t.Fatalf("gen exit=%d", code)
	}
	var code int
	out := captureStdout(t, func() { code = runCLI(t, "check", "--root", root) })
	if code != pipeline.ExitDamage {
		t.Fatalf("burst-loss exit=%d want 3", code)
	}
	for _, want := range []string{
		"manifest_segments=", "damage_ranges=1", "missing_packets=",
		"first_bad_segment=", "first_blocked_dependency=", "output_sha256=", "exit_code=3",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestJSONReportRoundTrips(t *testing.T) {
	root := t.TempDir()
	if code := runCLI(t, "gen", "--seed", "5", "--fault", "truncated-box", "--root", root); code != 0 {
		t.Fatalf("gen exit=%d", code)
	}
	var code int
	out := captureStdout(t, func() { code = runCLI(t, "check", "--root", root, "--json") })
	dec := json.NewDecoder(strings.NewReader(out))
	var rep pipeline.Report
	if err := dec.Decode(&rep); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out)
	}
	if code != pipeline.ExitDamage || rep.ExitCode != pipeline.ExitDamage {
		t.Fatalf("code=%d rep=%d", code, rep.ExitCode)
	}
	if rep.FirstBadSegment == "" || len(rep.DamageRanges) == 0 {
		t.Fatalf("report %+v", rep)
	}
	if rep.OutputSHA256 == "" {
		t.Fatal("missing output digest")
	}
}

func TestResumeMatchesChecksum(t *testing.T) {
	root := t.TempDir()
	cp := filepath.Join(root, "state", "cp.json")
	if code := runCLI(t, "gen", "--seed", "42", "--fault", "burst-loss", "--root", root); code != 0 {
		t.Fatalf("gen exit=%d", code)
	}
	var first string
	captureStdout(t, func() { runCLI(t, "check", "--root", root, "--checkpoint", cp) })
	first = digestLine(t, root, cp, false)
	second := digestLine(t, root, cp, true)
	if first != second {
		t.Fatalf("resume changed the output checksum: %s vs %s", first, second)
	}
}

// digestLine runs a check and returns the output_sha256 line.
func digestLine(t *testing.T, root, cp string, resume bool) string {
	t.Helper()
	args := []string{"check", "--root", root, "--checkpoint", cp}
	if resume {
		args = append(args, "--resume")
	}
	out := captureStdout(t, func() { runCLI(t, args...) })
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "output_sha256=") {
			return line
		}
	}
	t.Fatalf("no output_sha256 in:\n%s", out)
	return ""
}

func TestUnknownFaultIsInputError(t *testing.T) {
	root := t.TempDir()
	if code := runCLI(t, "gen", "--seed", "1", "--fault", "nonsense", "--root", root); code != pipeline.ExitInput {
		t.Fatalf("exit=%d want 2", code)
	}
}

func TestUnknownCommandIsInputError(t *testing.T) {
	if code := runCLI(t, "wat"); code != pipeline.ExitInput {
		t.Fatalf("exit=%d want 2", code)
	}
}
