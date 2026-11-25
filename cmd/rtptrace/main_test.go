package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func execute(t *testing.T, args ...string) (int, map[string]interface{}) {
	t.Helper()
	var out, err bytes.Buffer
	code := run(args, &out, &err)
	if err.Len() != 0 && t.Failed() {
		t.Logf("stderr: %s", err.String())
	}
	if out.Len() == 0 {
		t.Fatalf("no JSON output, code=%d stderr=%s", code, err.String())
	}
	var report map[string]interface{}
	if jerr := json.Unmarshal(out.Bytes(), &report); jerr != nil {
		t.Fatalf("decode output: %v\noutput head: %s", jerr, out.String()[:min(200, out.Len())])
	}
	return code, report
}

func TestCLINormalDemo(t *testing.T) {
	code, r := execute(t, "demo", "--seed", "17", "--duration", "4s", "--segment", "2s", "--bitrates", "800k,1600k", "--report", "json")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if r["recovery_state"] != "ok" {
		t.Fatalf("recovery_state=%v", r["recovery_state"])
	}
	if r["summary_sha256"].(string) == "" || len(r["summary_sha256"].(string)) != 64 {
		t.Fatalf("summary=%v", r["summary_sha256"])
	}
	if r["byte_error_total"].(float64) != 0 {
		t.Fatalf("byte error=%v", r["byte_error_total"])
	}
}

func TestCLITruncatedExitCodeAndLocation(t *testing.T) {
	code, r := execute(t, "demo", "--seed", "17", "--fault", "truncated-stbl", "--report", "json")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if r["failure_track"].(float64) != 1 || r["failure_sample"].(float64) != 37 {
		t.Fatalf("location=%v/%v", r["failure_track"], r["failure_sample"])
	}
	if !strings.HasPrefix(r["recovery_state"].(string), "recovery") {
		t.Fatalf("state=%v", r["recovery_state"])
	}
}

func TestCLIEditRecoveryExitCode(t *testing.T) {
	code, r := execute(t, "demo", "--seed", "17", "--fault", "edit-out-of-range", "--recover", "--report", "json")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if r["recovery_state"] != "recovered-edit-clamped" {
		t.Fatalf("state=%v", r["recovery_state"])
	}
	edits := r["edit_reports"].([]interface{})
	if len(edits) != 1 {
		t.Fatalf("edit reports=%d", len(edits))
	}
}

func TestCLIInvalidArgumentsUseOtherCode(t *testing.T) {
	var out, err bytes.Buffer
	code := run([]string{"demo", "--duration", "not-a-duration"}, &out, &err)
	if code == 0 || code == 2 {
		t.Fatalf("usage exit code=%d", code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
