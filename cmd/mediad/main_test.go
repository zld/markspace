package main

import (
	"encoding/json"
	"testing"

	"mediad/internal/model"
	"mediad/internal/pipeline"
)

func demoConfig(seed int64) commonFlags {
	return commonFlags{
		seed: seed, loss: 0.05, reorderMs: 80,
		lateWindow: 250, reportEvery: 0, cache: 512, fixture: "normal",
	}
}

func runSummary(t *testing.T, c commonFlags) model.Summary {
	t.Helper()
	sum, err := pipeline.Run(config(c), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return sum
}

func TestDemoAcceptancePasses(t *testing.T) {
	sum := runSummary(t, demoConfig(42))
	if fails := checkAcceptance(sum); len(fails) != 0 {
		t.Fatalf("acceptance failed: %v", fails)
	}
}

func TestSummaryJSONContract(t *testing.T) {
	sum := runSummary(t, demoConfig(42))
	b, err := json.Marshal(sum)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	required := []string{
		"av_sync_error_ms", "recovered_packets", "recovery_rate",
		"p95_latency_ms", "late_replayed", "malformed_packets",
		"index_rebuilt", "result_sha256",
	}
	for _, key := range required {
		if _, ok := got[key]; !ok {
			t.Errorf("summary JSON missing required field %q", key)
		}
	}
}

func TestRepeatDigestMatches(t *testing.T) {
	a := runSummary(t, demoConfig(42))
	b := runSummary(t, demoConfig(42))
	if a.ResultSHA256 != b.ResultSHA256 {
		t.Fatalf("digest drift: %s vs %s", a.ResultSHA256, b.ResultSHA256)
	}
}

func TestCheckAcceptanceFlagsFailures(t *testing.T) {
	bad := model.Summary{AVSyncErrorMs: 41, RecoveryRate: 0.1, P95LatencyMs: 251, MalformedPackets: 1}
	if fails := checkAcceptance(bad); len(fails) != 4 {
		t.Fatalf("fails = %v, want 4", fails)
	}
}

func TestCheckFixtureSignals(t *testing.T) {
	cases := []struct {
		name string
		sum  model.Summary
		want int
	}{
		{"corrupt", model.Summary{MalformedPackets: 1, BufferMalformed: 1, SyncMalformed: 1, EmittedSamples: 5}, 0},
		{"corrupt", model.Summary{MalformedPackets: 1, BufferMalformed: 1}, 2},
		{"late-rtcp", model.Summary{LateReplayed: 2, LateFeedback: 1}, 0},
		{"late-rtcp", model.Summary{}, 2},
		{"truncated", model.Summary{TruncatedTail: 1, MalformedPackets: 1}, 0},
		{"no-sr", model.Summary{AVSyncConfidence: "low"}, 0},
		{"wrap", model.Summary{QuarantinedPackets: 1}, 0},
		{"clock-drift", model.Summary{AVSyncConfidence: "high", ClockDriftPPM: 2000}, 0},
		{"clock-drift", model.Summary{AVSyncConfidence: "high"}, 1},
	}
	for _, c := range cases {
		if got := len(checkFixture(c.name, c.sum)); got != c.want {
			t.Errorf("%s: %d failures, want %d", c.name, got, c.want)
		}
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	cases := [][]string{
		{"--loss", "2"},
		{"--loss", "-0.1"},
		{"--reorder-ms", "-1"},
		{"--late-window-ms", "-1"},
		{"--cache", "0"},
		{"--report-every", "-1"},
		{"--fixture", "nope"},
		{"--seed", "-1"},
		{"extra"},
	}
	for _, args := range cases {
		fs := newDemoFlagSet()
		if _, err := parseCommon(fs, args, true); err == nil {
			t.Errorf("args %v accepted", args)
		}
	}
}

func TestParseAcceptsValidInput(t *testing.T) {
	fs := newDemoFlagSet()
	c, err := parseCommon(fs, []string{"--seed", "7", "--loss", "0", "--cache", "8"}, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.seed != 7 || c.loss != 0 || c.cache != 8 {
		t.Fatalf("parsed = %+v", c)
	}
}
