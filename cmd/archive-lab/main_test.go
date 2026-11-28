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
	if rep["report_sha256"] == "" || rep["report_sha256"] == nil {
		t.Fatal("report must carry a self digest for reproducibility checks")
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

func TestRecoveryPathAcrossReplacementAndMetadata(t *testing.T) {
	dir := t.TempDir()
	if _, _, code := runCLI(t, "fixture", "--dir", dir); code != 0 {
		t.Fatal("fixture failed")
	}
	swapped := func(from, to string) {
		if _, errs, code := runCLI(t, "swap", "--from", filepath.Join(dir, from), "--to", filepath.Join(dir, to)); code != 0 {
			t.Fatalf("swap %s: %s", from, errs)
		}
	}
	runNormal := func(meta string) map[string]interface{} {
		args := []string{"run", "--input", filepath.Join(dir, "normal.m3u8"),
			"--target-bps", "800000", "--max-bps", "900000",
			"--min-psnr", "35", "--budget-ms", "5000"}
		if meta != "" {
			args = append(args, "--metadata", meta)
		}
		out, errs, code := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("run exit=%d stderr=%s", code, errs)
		}
		var rep map[string]interface{}
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatal(err)
		}
		return rep
	}

	first := runNormal("")
	if first["cache"] != "miss" || first["index_rebuilt"] != true {
		t.Fatalf("normal: cache=%v rebuilt=%v", first["cache"], first["index_rebuilt"])
	}

	// Same path, new bytes: the index and cache must both be invalidated.
	swapped("media/replace", "media/normal")
	replaced := runNormal("")
	if replaced["cache"] != "miss" || replaced["index_rebuilt"] != true {
		t.Fatalf("replace: cache=%v rebuilt=%v", replaced["cache"], replaced["index_rebuilt"])
	}
	if replaced["input_digest"] == first["input_digest"] {
		t.Fatal("replaced segment must change the input digest")
	}

	// Same paths, updated encapsulation metadata: also a rebuild and miss.
	swapped("media/metadata", "media/normal")
	updated := runNormal("")
	if updated["cache"] != "miss" || updated["index_rebuilt"] != true {
		t.Fatalf("metadata: cache=%v rebuilt=%v", updated["cache"], updated["index_rebuilt"])
	}
	if updated["input_digest"] == replaced["input_digest"] {
		t.Fatal("metadata update must change the input digest")
	}

	// Unchanged repeat: no rebuild, cache hit, identical report digest.
	repeat := runNormal("")
	if repeat["cache"] != "hit" || repeat["index_rebuilt"] != false {
		t.Fatalf("repeat: cache=%v rebuilt=%v", repeat["cache"], repeat["index_rebuilt"])
	}
	if repeat["report_sha256"] != updated["report_sha256"] {
		t.Fatalf("repeat report digest differs: %v vs %v", repeat["report_sha256"], updated["report_sha256"])
	}

	// A metadata file outside the media bytes participates in the index too.
	runNormal("metadata.json")
	metaPath := filepath.Join(dir, "metadata.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, append(raw, []byte("{\"reencapsulated\":true}\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	metaChanged := runNormal("metadata.json")
	if metaChanged["index_rebuilt"] != true || metaChanged["cache"] != "miss" {
		t.Fatalf("metadata file change must rebuild and miss, got rebuilt=%v cache=%v",
			metaChanged["index_rebuilt"], metaChanged["cache"])
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
