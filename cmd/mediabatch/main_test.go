package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capture runs the CLI with stdout/stderr redirected and returns the exit code
// and captured output. It exercises the real entry path, not just packages.
func capture(t *testing.T, args ...string) (int, string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.String()
	}()
	code := run(args)
	w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return code, <-done
}

func TestEndToEndCLI(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	code, out := capture(t, "demo", "--work", work, "--chunk", "3")
	if code != 0 {
		t.Fatalf("demo exit=%d out=%s", code, out)
	}
	for _, want := range []string{"summary:", "segments=", "anomalies=", "duplicates=", "chunks="} {
		if !strings.Contains(out, want) {
			t.Fatalf("demo output missing %q: %s", want, out)
		}
	}

	// Material anomalies still produce a report, and the exit code stays 0.
	if _, err := os.Stat(filepath.Join(work, "report.json")); err != nil {
		t.Fatalf("report.json: %v", err)
	}

	code, out = capture(t, "verify", "--work", work)
	if code != 0 {
		t.Fatalf("verify exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "sorted-violations=0") || !strings.Contains(out, "missing-source=0") {
		t.Fatalf("verify out=%s", out)
	}
	var v1 map[string]interface{}
	data, _ := os.ReadFile(filepath.Join(work, "verify.json"))
	json.Unmarshal(data, &v1)

	code, out = capture(t, "verify", "--work", work)
	if code != 0 {
		t.Fatalf("second verify exit=%d", code)
	}
	var v2 map[string]interface{}
	data, _ = os.ReadFile(filepath.Join(work, "verify.json"))
	json.Unmarshal(data, &v2)
	if v1["digest"] != v2["digest"] {
		t.Fatalf("digest changed between runs: %v vs %v", v1["digest"], v2["digest"])
	}
}

func TestCrashResumeCLI(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	code, out := capture(t, "demo", "--work", work, "--chunk", "3", "--inject-crash", "after-artifact")
	if code != 0 {
		t.Fatalf("demo exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "crash-injected") {
		t.Fatalf("expected crash notice: %s", out)
	}

	code, out = capture(t, "resume", "--work", work)
	if code != 0 {
		t.Fatalf("resume exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "duplicate-publish=0") {
		t.Fatalf("resume out=%s", out)
	}
	if !strings.Contains(out, "adopted=") {
		t.Fatalf("resume should report adopted artifacts: %s", out)
	}

	code, out = capture(t, "resume", "--work", work)
	if code != 0 {
		t.Fatalf("second resume exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "new-artifacts=0") {
		t.Fatalf("second resume published artifacts: %s", out)
	}
}

func TestTimeRangeReport(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	if code, out := capture(t, "demo", "--work", work, "--chunk", "3", "--from", "8", "--to", "12"); code != 0 {
		t.Fatalf("demo exit=%d out=%s", code, out)
	}
	rep, err := os.ReadFile(filepath.Join(work, "report.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(rep), "\n") {
		if !strings.Contains(line, "[") {
			continue
		}
		// Every retained entry must overlap [8,12].
		if strings.Contains(line, "[0..4]") || strings.Contains(line, "[16..20]") {
			t.Fatalf("entry outside range: %s", line)
		}
	}
}

func TestExitCodes(t *testing.T) {
	if code, _ := capture(t, "demo"); code == 0 {
		t.Fatal("missing --work must fail")
	}
	if code, _ := capture(t, "demo", "--work", t.TempDir(), "--chunk", "0"); code == 0 {
		t.Fatal("bad chunk size must fail")
	}
	if code, _ := capture(t, "bogus"); code == 0 {
		t.Fatal("unknown command must fail")
	}
	// Corrupt index: non-zero exit, no report rewrite.
	work := filepath.Join(t.TempDir(), "demo")
	if code, out := capture(t, "demo", "--work", work, "--chunk", "3"); code != 0 {
		t.Fatalf("setup demo: %d %s", code, out)
	}
	os.WriteFile(filepath.Join(work, "index.json"), []byte("{corrupt"), 0o644)
	if code, out := capture(t, "verify", "--work", work); code == 0 {
		t.Fatalf("corrupt index should fail: %s", out)
	}
	if code, out := capture(t, "resume", "--work", work); code == 0 {
		t.Fatalf("resume on corrupt index should fail: %s", out)
	}
}

func TestManifestParseFailureExitsNonZero(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	if code, out := capture(t, "demo", "--work", work, "--chunk", "3"); code != 0 {
		t.Fatalf("setup: %d %s", code, out)
	}
	// Corrupt the MPD: the report is still written for everything else, but
	// the run must signal the parse failure.
	mpd := filepath.Join(work, "input", "dash", "manifest.mpd")
	if err := os.WriteFile(mpd, []byte("<MPD><Period>"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := capture(t, "resume", "--work", work)
	if code == 0 {
		t.Fatalf("manifest parse failure must exit non-zero: %s", out)
	}
	if _, err := os.Stat(filepath.Join(work, "report.json")); err != nil {
		t.Fatalf("report should still exist: %v", err)
	}
}

func TestUnknownContainerIsolatesSegment(t *testing.T) {
	work := filepath.Join(t.TempDir(), "demo")
	if code, out := capture(t, "demo", "--work", work, "--chunk", "3"); code != 0 {
		t.Fatalf("setup: %d %s", code, out)
	}
	// Overwrite one segment with bytes that are neither MP4 nor RTP.
	target := filepath.Join(work, "input", "hls", "seg-3.m4s")
	if err := os.WriteFile(target, []byte("not-media"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := capture(t, "resume", "--work", work)
	if code != 0 {
		t.Fatalf("corrupt media should still produce a report: %d %s", code, out)
	}
	rep, err := os.ReadFile(filepath.Join(work, "report.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rep), "unrecognised segment container") {
		t.Fatalf("report missing container anomaly:\n%s", rep)
	}
}
