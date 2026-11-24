package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestDemoCommandPrintsAcceptanceJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := filepath.Join(t.TempDir(), "acceptance")
	rc := run([]string{
		"demo", "--seed", "17", "--resume", "--out", dir,
		"--faults", "duplicate,reorder,burst-loss,cc-jump,psi-version,truncate",
	}, &stdout, &stderr)
	if rc != 2 {
		t.Fatalf("rc=%d stderr=%s stdout=%s", rc, stderr.String(), stdout.String())
	}
	var report map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	for _, key := range []string{
		"sync_error_ms", "loss_recovery_rate", "p95_latency_ms", "latency_samples",
		"dropped_duplicates", "gap_events", "psi_version", "output_sha256",
		"rollback_count", "exit_code",
	} {
		if _, ok := report[key]; !ok {
			t.Errorf("missing JSON field %q", key)
		}
	}
	if report["exit_code"].(float64) != 2 {
		t.Fatalf("json exit_code=%v", report["exit_code"])
	}
}

func TestUsageExitCode(t *testing.T) {
	rc := run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if rc != 1 {
		t.Fatalf("usage rc=%d want 1", rc)
	}
}
