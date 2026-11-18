package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parseLines(t *testing.T, data []byte) (frames []map[string]any, summary map[string]any) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var obj struct {
			Type    string         `json:"type"`
			Frame   map[string]any `json:"frame"`
			Summary map[string]any `json:"summary"`
		}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("bad JSON line %q: %v", line, err)
		}
		switch obj.Type {
		case "frame":
			frames = append(frames, obj.Frame)
		case "summary":
			if summary != nil {
				t.Fatal("more than one summary line")
			}
			summary = obj.Summary
		default:
			t.Fatalf("unknown line type %q", obj.Type)
		}
	}
	return frames, summary
}

func TestReplayHappyPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	var stderr bytes.Buffer
	code := run([]string{
		"replay", "--frames", "200", "--frame-ms", "20",
		"--target-ms", "80", "--max-wait-ms", "240",
		"--loss", "0.04", "--reorder", "0.08", "--duplicate", "0.02",
		"--seed", "41", "--out", path,
	}, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	frames, summary := parseLines(t, data)
	if len(frames) != 200 {
		t.Fatalf("frame lines = %d, want 200", len(frames))
	}
	if summary == nil {
		t.Fatal("missing summary line")
	}
	for _, key := range []string{"buffer_ms", "input_packets", "dropped_packets",
		"concealed_frames", "max_conceal_run", "continuity_delta"} {
		if _, ok := summary[key]; !ok {
			t.Errorf("summary missing %q", key)
		}
	}
	for _, key := range []string{"buffer_ms", "target_ms", "seq", "kind"} {
		if _, ok := frames[0][key]; !ok {
			t.Errorf("frame missing %q", key)
		}
	}
	if summary["seed"].(float64) != 41 {
		t.Errorf("seed = %v, want 41", summary["seed"])
	}
}

func TestReplayIsReproducible(t *testing.T) {
	dir := t.TempDir()
	args := []string{"replay", "--frames", "150", "--frame-ms", "20",
		"--target-ms", "80", "--loss", "0.05", "--reorder", "0.05",
		"--duplicate", "0.02", "--seed", "7"}
	var a, b bytes.Buffer
	argsA := append(append([]string{}, args...), "--out", filepath.Join(dir, "a.jsonl"))
	argsB := append(append([]string{}, args...), "--out", filepath.Join(dir, "b.jsonl"))
	if code := run(argsA, &a); code != exitOK {
		t.Fatalf("first exit = %d: %s", code, a.String())
	}
	if code := run(argsB, &b); code != exitOK {
		t.Fatalf("second exit = %d: %s", code, b.String())
	}
	da, _ := os.ReadFile(filepath.Join(dir, "a.jsonl"))
	db, _ := os.ReadFile(filepath.Join(dir, "b.jsonl"))
	if !bytes.Equal(da, db) {
		t.Fatal("same seed produced different reports")
	}
}

func TestReplayStdout(t *testing.T) {
	// Swapping os.Stdout is the only way to observe the stdout path; the
	// test restores it immediately and runs synchronously.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		var buf bytes.Buffer
		buf.ReadFrom(r)
		done <- buf.Bytes()
	}()
	var stderr bytes.Buffer
	code := run([]string{"replay", "--frames", "10", "--frame-ms", "20",
		"--target-ms", "40", "--seed", "1", "--out", "-"}, &stderr)
	w.Close()
	os.Stdout = old
	out := <-done
	if code != exitOK {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if !strings.Contains(string(out), `"type":"summary"`) {
		t.Fatalf("stdout = %q", out)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"no command":        {},
		"unknown command":   {"frobnicate"},
		"zero frames":       {"replay", "--frames", "0"},
		"negative frame":    {"replay", "--frame-ms", "-1"},
		"target over wait":  {"replay", "--target-ms", "400", "--max-wait-ms", "200"},
		"window over max":   {"replay", "--window", "99999"},
		"loss out of range": {"replay", "--loss", "1.5"},
		"bad reorder delay": {"replay", "--reorder-delay", "-2"},
		"bad invalid run":   {"replay", "--max-invalid-run", "0"},
		"unknown flag":      {"replay", "--nope"},
		"extra argument":    {"replay", "extra"},
		"bad duration":      {"live", "--duration", "-1s", "--frames", "5"},
	}
	for name, args := range cases {
		var stderr bytes.Buffer
		if code := run(args, &stderr); code != exitUsage {
			t.Errorf("%s: exit = %d, want %d", name, code, exitUsage)
		}
	}
}

func TestHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"replay", "-h"}, {"live", "--help"}} {
		var stderr bytes.Buffer
		if code := run(args, &stderr); code != exitOK {
			t.Errorf("args %v: exit = %d", args, code)
		}
	}
}

func TestDamagedStreamExitsThree(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"replay", "--frames", "100", "--frame-ms", "20",
		"--target-ms", "40", "--corrupt", "1", "--max-invalid-run", "5",
		"--seed", "3", "--out", filepath.Join(t.TempDir(), "bad.jsonl")}, &stderr)
	if code != exitDamaged {
		t.Fatalf("exit = %d, want %d; stderr = %s", code, exitDamaged, stderr.String())
	}
	if !strings.Contains(stderr.String(), "damaged") {
		t.Fatalf("stderr = %q, want damage report", stderr.String())
	}
}

func TestOutputFailureExitsFour(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir", "out.jsonl")
	var stderr bytes.Buffer
	code := run([]string{"replay", "--frames", "20", "--frame-ms", "20",
		"--target-ms", "40", "--seed", "1", "--out", missing}, &stderr)
	if code != exitOutput {
		t.Fatalf("exit = %d, want %d", code, exitOutput)
	}
}
