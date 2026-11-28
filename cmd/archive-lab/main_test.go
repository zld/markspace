package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"archive-lab/internal/media"
)

func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	err := run(args, &out, &errb)
	return out.String(), errb.String(), media.ExitCode(err)
}

func TestAcceptanceFlow(t *testing.T) {
	dir := t.TempDir()
	if _, _, code := runCLI(t, "fixture", "--dir", dir); code != 0 {
		t.Fatalf("fixture exit = %d", code)
	}
	manifest := filepath.Join(dir, "normal.m3u8")
	// Seed an index left behind by an earlier run, then corrupt it: the
	// recovery path must fall back to a full scan rather than trust it.
	args := []string{"run", "--input", manifest,
		"--target-bps", "800000", "--max-bps", "900000",
		"--min-psnr", "35", "--budget-ms", "5000"}
	out, errs, code := runCLI(t, args...)
	if code != 0 {
		t.Fatalf("run exit = %d stderr=%s", code, errs)
	}
	var rep map[string]interface{}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("report is not JSON: %v\n%s", err, out)
	}
	if rep["status"] != "ok" {
		t.Fatalf("status = %v", rep["status"])
	}

	// Corrupt index: still succeeds, with index_rebuilt = true.
	indexPath := filepath.Join(dir, ".archive-lab", "index.json")
	if err := os.WriteFile(indexPath, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code = runCLI(t, args...)
	if code != 0 {
		t.Fatalf("corrupt index run exit = %d", code)
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep["index_rebuilt"] != true {
		t.Fatalf("corrupt index must force a rebuild, got %v", rep["index_rebuilt"])
	}

	// Repeat: cache hit, no rebuild.
	out, _, code = runCLI(t, args...)
	if code != 0 {
		t.Fatalf("repeat exit = %d", code)
	}
	_ = json.Unmarshal([]byte(out), &rep)
	if rep["cache"] != "hit" || rep["index_rebuilt"] != false {
		t.Fatalf("repeat should hit cache without rebuild, got cache=%v rebuilt=%v", rep["cache"], rep["index_rebuilt"])
	}
}

func TestUsageErrorsExitOne(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"bogus"},
		{"run"},
		{"run", "--input", "x.m3u8", "--target-bps", "900000", "--max-bps", "800000", "--min-psnr", "35", "--budget-ms", "10"},
		{"run", "--input", "x.m3u8", "--target-bps", "-1", "--max-bps", "800000", "--min-psnr", "35", "--budget-ms", "10"},
	} {
		_, _, code := runCLI(t, args...)
		if code != 1 {
			t.Fatalf("args %v exit = %d, want 1", args, code)
		}
	}
}

func TestCorruptInputsExitTwo(t *testing.T) {
	dir := t.TempDir()
	if _, _, code := runCLI(t, "fixture", "--dir", dir); code != 0 {
		t.Fatal("fixture failed")
	}
	for _, name := range []string{"truncated.m3u8", "malformed.mpd"} {
		out, errs, code := runCLI(t, "run", "--input", filepath.Join(dir, name),
			"--target-bps", "800000", "--max-bps", "900000",
			"--min-psnr", "35", "--budget-ms", "5000")
		if code != 2 {
			t.Fatalf("%s exit = %d (stderr %s)", name, code, errs)
		}
		var rep map[string]interface{}
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("%s report is not JSON: %v", name, err)
		}
		if rep["status"] != "error" || rep["failed_stage"] != "parse" {
			t.Fatalf("%s report = %+v", name, rep)
		}
		if err2 := rep["error"].(string); err2 == "" {
			t.Fatalf("%s has no error detail", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".archive-lab", "index.json")); !os.IsNotExist(err) {
		t.Fatal("damaged inputs must not commit an index")
	}
}
