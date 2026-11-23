package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// invoke runs the CLI in-process and captures stdout/stderr.
func invoke(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = run(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

// decode parses the JSON document printed on stdout.
func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, s)
	}
	return m
}

func TestDemoCommandFieldsAndExit(t *testing.T) {
	code, stdout, stderr := invoke(t, "demo", "--seed", "42", "--slow-consumer-ms", "120", "--loss-every", "17")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	m := decode(t, stdout)
	for _, key := range []string{
		"sync_error_ms", "packet_recovery_rate", "p95_latency_ms",
		"index_entries", "boundary_mismatch",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing field %q", key)
		}
	}
	if got := m["sync_error_ms"].(float64); got > 20 {
		t.Errorf("sync_error_ms = %v > 20", got)
	}
	if got := m["packet_recovery_rate"].(float64); got < 0.90 {
		t.Errorf("packet_recovery_rate = %v < 0.90", got)
	}
	if got := m["p95_latency_ms"].(float64); got > 250 {
		t.Errorf("p95_latency_ms = %v > 250", got)
	}
	if got := m["index_entries"].(float64); got < 10 {
		t.Errorf("index_entries = %v < 10", got)
	}
	if got := m["boundary_mismatch"].(float64); got != 0 {
		t.Errorf("boundary_mismatch = %v, want 0", got)
	}
}

func TestCorruptCommandUsesAliasAndExitsTwo(t *testing.T) {
	// The acceptance command uses --corrupt, not --fault.
	code, stdout, stderr := invoke(t, "corrupt", "--seed", "42", "--corrupt", "cluster-truncate")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr: %s)", code, stderr)
	}
	m := decode(t, stdout)
	if got := m["error_code"]; got != "truncated_cluster" {
		t.Fatalf("error_code = %v", got)
	}
	if got := m["recoverable_prefix_bytes"].(float64); got <= 0 {
		t.Fatalf("recoverable_prefix_bytes = %v, want > 0", got)
	}
	if !strings.Contains(stderr, "truncated_cluster") {
		t.Errorf("stderr does not explain the damage: %s", stderr)
	}
}

func TestCorruptCommandFaultFlagAgreesWithAlias(t *testing.T) {
	code, _, stderr := invoke(t, "corrupt", "--fault", "rtp-loss", "--corrupt", "cluster-truncate")
	if code != 1 {
		t.Fatalf("conflicting flags exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "disagree") {
		t.Errorf("stderr = %s", stderr)
	}
}

func TestVerifyReportsHashEquality(t *testing.T) {
	code, stdout, stderr := invoke(t, "verify", "--repeat", "2")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	m := decode(t, stdout)
	if got := m["summary_hash_equal"]; got != true {
		t.Fatalf("summary_hash_equal = %v", got)
	}
	if got := m["index_digest_equal"]; got != true {
		t.Fatalf("index_digest_equal = %v", got)
	}
	if got := m["summary_hash"].(string); got == "" {
		t.Fatal("empty summary hash")
	}
	hashes := m["summary_hashes"].([]any)
	if len(hashes) != 2 || hashes[0] != hashes[1] {
		t.Fatalf("summary_hashes = %v", hashes)
	}
}

func TestVerifyRejectsLowRepeat(t *testing.T) {
	if code, _, _ := invoke(t, "verify", "--repeat", "1"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	code, _, stderr := invoke(t, "frobnicate")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("stderr = %s", stderr)
	}
}

func TestHelpExitsZero(t *testing.T) {
	code, stdout, _ := invoke(t, "help")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, cmd := range []string{"demo", "corrupt", "verify"} {
		if !strings.Contains(stdout, cmd) {
			t.Errorf("help does not mention %q", cmd)
		}
	}
}

func TestDemoAcceptsDocumentedCorruptFlag(t *testing.T) {
	// The documented demo invocation passes --corrupt cluster-truncate and
	// expects a recovery summary with exit code 2.
	code, stdout, stderr := invoke(t, "demo", "--seed", "42", "--corrupt", "cluster-truncate")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr: %s)", code, stderr)
	}
	m := decode(t, stdout)
	if got := m["error_code"]; got != "truncated_cluster" {
		t.Fatalf("error_code = %v", got)
	}
	if got := m["recoverable_prefix_bytes"].(float64); got <= 0 {
		t.Fatalf("recoverable_prefix_bytes = %v", got)
	}
}

func TestDemoConflictingFaultFlags(t *testing.T) {
	code, _, stderr := invoke(t, "demo", "--fault", "rtp-loss", "--corrupt", "cluster-truncate")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "disagree") {
		t.Errorf("stderr = %s", stderr)
	}
}

func TestDemoNegativeFlagsRejected(t *testing.T) {
	if code, _, _ := invoke(t, "demo", "--loss-every", "-1"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
