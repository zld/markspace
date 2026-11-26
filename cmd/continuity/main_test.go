package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"continuity/internal/fixture"
)

func runDemo(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := dispatch(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestDemoNormal(t *testing.T) {
	code, out, _ := runDemo(t, "demo", "--case", "normal", "--seed", "17")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	sum := out[strings.LastIndex(out, "summary"):]
	for _, kv := range []string{"mapped=6", "emitted=5", "duplicate=1", "lost=0",
		"p50_delay_ms=40", "drift_ppm=80", "max_abs_byte_delta=0"} {
		if !strings.Contains(sum, kv) {
			t.Fatalf("summary missing %q: %s", kv, sum)
		}
	}
	if n := strings.Count(out, "sha256="); n != 6 {
		t.Fatalf("mapping rows with sha256: %d, want 6", n)
	}
}

func TestDemoDamaged(t *testing.T) {
	code, _, errOut := runDemo(t, "demo", "--case", "damaged", "--seed", "17")
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	for _, tok := range []string{"segment[2]", "byte=24", "truncated_box"} {
		if !strings.Contains(errOut, tok) {
			t.Fatalf("stderr missing %q: %s", tok, errOut)
		}
	}
}

func TestDemoRecoverDeterministic(t *testing.T) {
	code1, out1, _ := runDemo(t, "demo", "--case", "recover", "--seed", "17")
	code2, out2, _ := runDemo(t, "demo", "--case", "recover", "--seed", "17")
	if code1 != 0 || code2 != 0 {
		t.Fatalf("exits: %d %d", code1, code2)
	}
	if out1 != out2 {
		t.Fatal("two recover runs differ")
	}
	for _, kv := range []string{"lost=1", "gap_ms=2000", "resumed=1"} {
		if !strings.Contains(out1, kv) {
			t.Fatalf("summary missing %q", kv)
		}
	}
	d := out1[strings.LastIndex(out1, "digest=")+7:]
	d = strings.TrimSpace(d)
	if len(d) != 64 {
		t.Fatalf("digest len=%d", len(d))
	}
}

func TestRunWithDASHManifest(t *testing.T) {
	dir := t.TempDir()
	if err := fixture.Generate(dir, "normal", 17); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := dispatch([]string{"run",
		"--manifest", filepath.Join(dir, "index.mpd"),
		"--events", filepath.Join(dir, "events.rtp"),
		"--arrivals", filepath.Join(dir, "arrivals.tsv")}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "mapped=6") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestUsageError(t *testing.T) {
	code, _, _ := runDemo(t, "demo", "--case", "bogus")
	if code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
}
