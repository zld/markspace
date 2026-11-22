package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// captureStdout runs fn while the process stdout is redirected, returning
// everything printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	done := make(chan string, 1)
	go func() {
		buf := new(strings.Builder)
		chunk := make([]byte, 4096)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				buf.Write(chunk[:n])
			}
			if err != nil {
				break
			}
		}
		done <- buf.String()
	}()
	fn()
	w.Close()
	out := <-done
	r.Close()
	return out
}

// fixtureKindsForTest mirrors the generator's list without importing it, so a
// kind added to the generator fails this test loudly.
func fixtureKindsForTest() []string {
	return []string{"clean", "no-moov", "truncated", "bad-range", "dep-cycle", "wrap", "no-index", "garbled"}
}

func TestExitCodesMatchContract(t *testing.T) {
	cases := []struct {
		fixture string
		want    int
	}{
		{"clean", 0},
		{"no-index", 0},
		{"no-moov", 0},
		{"truncated", 0},
		{"bad-range", 0},
		{"wrap", 0},
		{"dep-cycle", 2},
		{"garbled", 3},
	}
	for _, c := range cases {
		got := run([]string{"demo", "--fixture", c.fixture, "--dir", t.TempDir()})
		if got != c.want {
			t.Errorf("%s: exit = %d, want %d", c.fixture, got, c.want)
		}
	}
}

func TestUnknownFixtureAndLayoutFail(t *testing.T) {
	if got := run([]string{"demo", "--fixture", "does-not-exist", "--dir", t.TempDir()}); got != 1 {
		t.Errorf("unknown fixture exit = %d, want 1", got)
	}
	if got := run([]string{"demo", "--channels", "9.9", "--dir", t.TempDir()}); got != 1 {
		t.Errorf("unknown layout exit = %d, want 1", got)
	}
	if got := run([]string{"demo", "--sample-rate", "0", "--dir", t.TempDir()}); got != 1 {
		t.Errorf("zero rate exit = %d, want 1", got)
	}
}

func TestDemoWritesManifest(t *testing.T) {
	out := t.TempDir() + "/manifest.json"
	code := run([]string{"demo", "--fixture", "clean", "--out", out, "--dir", t.TempDir()})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"target_kbps":128`, `"actual_kbps":128`, `"summary":"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("manifest missing %s", want)
		}
	}
}

func TestFixturesCommandWritesEveryKind(t *testing.T) {
	dir := t.TempDir()
	if code := run([]string{"fixtures", "--dir", dir}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(fixtureKindsForTest()) {
		t.Fatalf("wrote %d fixtures, want %d", len(entries), len(fixtureKindsForTest()))
	}
}

// TestAcceptanceLineShape checks the exact key=value contract documented for
// the demo command by capturing stdout.
func TestAcceptanceLineShape(t *testing.T) {
	line := captureStdout(t, func() {
		run([]string{"demo", "--fixture", "clean", "--dir", t.TempDir()})
	})
	line = strings.SplitN(line, "\n", 2)[0]
	re := regexp.MustCompile(`status=ok target_kbps=128 actual_kbps=[0-9.]+ bitrate_error_pct=-?[0-9.]+ input_lufs=-?[0-9.]+ output_lufs=-?[0-9.]+ clipped_samples=[0-9]+ gaps=0 manifest_alignment_ms=[0-9.]+`)
	if !re.MatchString(line) {
		t.Fatalf("acceptance line does not match contract:\n%s", line)
	}
}

func TestRecoveredLineShape(t *testing.T) {
	line := captureStdout(t, func() {
		run([]string{"demo", "--fixture", "truncated", "--dir", t.TempDir()})
	})
	line = strings.SplitN(line, "\n", 2)[0]
	if !strings.HasPrefix(line, "status=recovered ") {
		t.Fatalf("status line: %s", line)
	}
	if !regexp.MustCompile(`gaps=[1-9][0-9]* manifest_alignment_ms=[0-9.]+ valid_samples=[1-9][0-9]*`).MatchString(line) {
		t.Fatalf("recovered line missing positive gaps/valid samples:\n%s", line)
	}
}
