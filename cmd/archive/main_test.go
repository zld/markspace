package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// runCapture runs the command in-process and captures stdout.
func runCapture(fn func([]string) int, args ...string) (int, string) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stdout = w
	code := fn(args)
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return code, buf.String()
}

func valueOf(t *testing.T, out, key string) string {
	t.Helper()
	re := regexp.MustCompile(key + `=([^\s]+)`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("output missing %q:\n%s", key, out)
	}
	return m[1]
}

func TestDemoCleanRunExitsZero(t *testing.T) {
	outDir := t.TempDir()
	ckpt := filepath.Join(t.TempDir(), "run.ckpt")
	code, out := runCapture(run, "demo", "--out", outDir, "--checkpoint", ckpt)
	if code != exitOK {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	trackCount, _ := strconv.Atoi(valueOf(t, out, "track_count"))
	mappedCount, _ := strconv.Atoi(valueOf(t, out, "mapped_count"))
	if trackCount != 5 || mappedCount != 3 {
		t.Fatalf("track_count=%d mapped_count=%d", trackCount, mappedCount)
	}
	if valueOf(t, out, "payload_match") != "true" {
		t.Fatalf("payload changed across remux:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(outDir, "restored.mp4")); err != nil {
		t.Fatalf("output missing: %v", err)
	}
	st, err := loadCheckpoint(ckpt)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Completed {
		t.Fatal("checkpoint not marked complete")
	}
}

func TestDemoCorruptRunExitsOneAndReportsAllFields(t *testing.T) {
	outDir := t.TempDir()
	ckpt := filepath.Join(t.TempDir(), "run.ckpt")
	code, out := runCapture(run, "demo", "--out", outDir, "--checkpoint", ckpt, "--corrupt", "truncate,segment,index")
	if code != exitUnrecoverable {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	for _, key := range []string{"error_code=", "offset_bytes=", "track_id=", "start_ns=", "end_ns="} {
		if !strings.Contains(out, key) {
			t.Fatalf("output missing %q:\n%s", key, out)
		}
	}
	if !strings.Contains(out, "error_code=corrupt_segment") || !strings.Contains(out, "error_code=truncated_box") || !strings.Contains(out, "error_code=missing_index") {
		t.Fatalf("not all damage kinds reported:\n%s", out)
	}
	inj, err := strconv.Atoi(valueOf(t, out, "injected_ranges"))
	if err != nil {
		t.Fatal(err)
	}
	cov, _ := strconv.Atoi(valueOf(t, out, "covered_ranges"))
	if inj == 0 || cov != inj {
		t.Fatalf("coverage %d/%d", cov, inj)
	}
}

func TestDemoIsReproducible(t *testing.T) {
	outA, outB := t.TempDir(), t.TempDir()
	ckptA := filepath.Join(t.TempDir(), "a.ckpt")
	ckptB := filepath.Join(t.TempDir(), "b.ckpt")
	argsA := []string{"demo", "--out", outA, "--checkpoint", ckptA, "--corrupt", "truncate,segment,index"}
	argsB := []string{"demo", "--out", outB, "--checkpoint", ckptB, "--corrupt", "truncate,segment,index"}
	_, a := runCapture(run, argsA...)
	_, b := runCapture(run, argsB...)
	if valueOf(t, a, "payload_sha256") != valueOf(t, b, "payload_sha256") {
		t.Fatal("payload digest differs between identical runs")
	}
	if valueOf(t, a, "output_sha256") != valueOf(t, b, "output_sha256") {
		t.Fatal("output checksum differs between identical runs")
	}
}

func TestDemoInterruptResumeMatchesFullRun(t *testing.T) {
	fullDir, resumeDir := t.TempDir(), t.TempDir()
	fullCkpt := filepath.Join(t.TempDir(), "full.ckpt")
	resumeCkpt := filepath.Join(t.TempDir(), "resume.ckpt")
	_, full := runCapture(run, "demo", "--out", fullDir, "--checkpoint", fullCkpt, "--corrupt", "truncate,segment,index")
	_, resumed := runCapture(run, "demo", "--out", resumeDir, "--checkpoint", resumeCkpt, "--corrupt", "truncate,segment,index", "--interrupt")
	if valueOf(t, full, "output_sha256") != valueOf(t, resumed, "output_sha256") {
		t.Fatalf("resume checksum %s != full %s", valueOf(t, resumed, "output_sha256"), valueOf(t, full, "output_sha256"))
	}
	if !strings.Contains(resumed, "resume_ms=") || !strings.Contains(resumed, "reused_tracks=") {
		t.Fatalf("resume summary missing:\n%s", resumed)
	}
	st, err := loadCheckpoint(resumeCkpt)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tr := range st.Tracks {
		for _, f := range tr.Fragments {
			key := tr.Key + "/" + strconv.Itoa(f.Seq)
			if seen[key] {
				t.Fatalf("fragment %s recorded twice", key)
			}
			seen[key] = true
		}
	}
}

func TestDemoRejectsChangedInputOnResume(t *testing.T) {
	outDir := t.TempDir()
	ckpt := filepath.Join(t.TempDir(), "run.ckpt")
	runCapture(run, "demo", "--out", outDir, "--checkpoint", ckpt, "--corrupt", "segment")
	code, out := runCapture(run, "demo", "--out", outDir, "--checkpoint", ckpt, "--corrupt", "segment,truncate")
	if code != exitInvalid {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "resume_rejected") || !strings.Contains(out, "checkpoint_mismatch") {
		t.Fatalf("mismatch not reported:\n%s", out)
	}
}

func TestDemoRejectsBadArguments(t *testing.T) {
	cases := [][]string{
		{"demo", "--format", "wav"},
		{"demo", "--corrupt", "bogus"},
		{"demo", "--workers", "0"},
		{"nonsense"},
		{},
	}
	for _, args := range cases {
		code, _ := runCapture(run, args...)
		if code != exitInvalid {
			t.Fatalf("args %v: exit = %d", args, code)
		}
	}
}

// loadCheckpoint decodes the checkpoint with the same shape the command
// writes, without importing the internal package into a CLI test.
func loadCheckpoint(path string) (*struct {
	Completed bool `json:"completed"`
	Tracks    []struct {
		Key       string `json:"key"`
		Fragments []struct {
			Seq int `json:"seq"`
		} `json:"fragments"`
	} `json:"tracks"`
}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st struct {
		Completed bool `json:"completed"`
		Tracks    []struct {
			Key       string `json:"key"`
			Fragments []struct {
				Seq int `json:"seq"`
			} `json:"fragments"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	return &st, nil
}
