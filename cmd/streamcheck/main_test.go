package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEndToEnd mirrors the acceptance flow: checkpoint stop at 75,
// resume to 0, deterministic rerun with identical recovered output.
func TestEndToEnd(t *testing.T) {
	work1 := filepath.Join(t.TempDir(), "pwf")
	work2 := filepath.Join(t.TempDir(), "pwf2")

	var out bytes.Buffer
	rc := run([]string{"demo", "--seed", "17", "--fault-set", "all", "--work", work1, "--stop-after", "inspect"}, &out)
	if rc != 75 {
		t.Fatalf("stop-after rc=%d, want 75\n%s", rc, out.String())
	}
	for _, want := range []string{"duplicates=1", "reordered-in-window=2", "burst-lost=3",
		"discontinuities=1", "offset-error=0B", "gop-max-normal=2000ms", "gop-max-after-injection=4000ms"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("report missing %q\n%s", want, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(work1, "recovered.bin")); !os.IsNotExist(err) {
		t.Fatal("recovered.bin must not exist before migrate stage")
	}

	out.Reset()
	rc = run([]string{"resume", "--work", work1}, &out)
	if rc != 0 {
		t.Fatalf("resume rc=%d\n%s", rc, out.String())
	}
	if !strings.Contains(out.String(), "rollback-bytes-identical=true") {
		t.Fatalf("resume report missing rollback proof\n%s", out.String())
	}
	if !strings.Contains(out.String(), "result: OK") {
		t.Fatalf("resume did not pass verification\n%s", out.String())
	}

	out.Reset()
	rc = run([]string{"demo", "--seed", "17", "--fault-set", "all", "--work", work2}, &out)
	if rc != 0 {
		t.Fatalf("full run rc=%d\n%s", rc, out.String())
	}
	b1, err := os.ReadFile(filepath.Join(work1, "recovered.bin"))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(filepath.Join(work2, "recovered.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatal("same seed must produce identical recovered.bin")
	}
}

func TestUsageErrors(t *testing.T) {
	var out bytes.Buffer
	if rc := run(nil, &out); rc != 2 {
		t.Fatalf("no args rc=%d", rc)
	}
	if rc := run([]string{"bogus"}, &out); rc != 2 {
		t.Fatalf("bad command rc=%d", rc)
	}
	if rc := run([]string{"demo", "--seed", "1"}, &out); rc != 2 {
		t.Fatalf("missing --work rc=%d", rc)
	}
	if rc := run([]string{"demo", "--work", filepath.Join(t.TempDir(), "w"), "--fault-set", "bogus"}, &out); rc != 2 {
		t.Fatalf("bad fault-set rc=%d", rc)
	}
	if rc := run([]string{"resume", "--work", t.TempDir()}, &out); rc != 2 {
		t.Fatalf("resume without checkpoint rc=%d", rc)
	}
}

func TestFaultSetNone(t *testing.T) {
	work := filepath.Join(t.TempDir(), "clean")
	var out bytes.Buffer
	rc := run([]string{"demo", "--seed", "5", "--fault-set", "none", "--work", work}, &out)
	if rc != 0 {
		t.Fatalf("clean run rc=%d\n%s", rc, out.String())
	}
	for _, want := range []string{"duplicates=0", "reordered-in-window=0", "burst-lost=0",
		"corrupt-segment=none", "result: OK"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("clean report missing %q\n%s", want, out.String())
		}
	}
}

func TestResumeRejectsTamperedInput(t *testing.T) {
	work := filepath.Join(t.TempDir(), "pwf")
	var out bytes.Buffer
	if rc := run([]string{"demo", "--seed", "17", "--fault-set", "all", "--work", work, "--stop-after", "inspect"}, &out); rc != 75 {
		t.Fatalf("rc=%d", rc)
	}
	// Tamper with a fixture input after the checkpoint.
	p := filepath.Join(work, "live.m3u8")
	data, _ := os.ReadFile(p)
	if err := os.WriteFile(p, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if rc := run([]string{"resume", "--work", work}, &out); rc != 2 {
		t.Fatalf("tampered input rc=%d, want 2\n%s", rc, out.String())
	}
}
