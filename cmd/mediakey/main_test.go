package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runCLI invokes run with captured streams.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

type decodedReport struct {
	AssetID       string `json:"asset_id"`
	Verdict       string `json:"verdict"`
	RotationCount int    `json:"rotation_count"`
	SegmentCount  int    `json:"segment_count"`
	OKCount       int    `json:"ok_count"`
	FailedCount   int    `json:"failed_count"`
	License       struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"license"`
	Rotations []struct {
		Rotation  int    `json:"rotation"`
		FromKeyID string `json:"from_key_id"`
		ToKeyID   string `json:"to_key_id"`
		AtSegment int    `json:"at_segment"`
	} `json:"rotations"`
	Segments []struct {
		SegmentIndex int    `json:"segment_index"`
		KeyID        string `json:"key_id"`
		Status       string `json:"status"`
		Reason       string `json:"reason"`
	} `json:"segments"`
	Replay struct {
		Checked   bool `json:"checked"`
		Successes int  `json:"successes"`
		Replays   int  `json:"replays"`
		Rejected  bool `json:"rejected"`
	} `json:"replay"`
	Boundary struct {
		Checked       bool   `json:"checked"`
		LicenseReason string `json:"license_reason"`
	} `json:"boundary_check"`
}

func decodeJSON(t *testing.T, out string) decodedReport {
	t.Helper()
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON in output:\n%s", out)
	}
	var r decodedReport
	if err := json.Unmarshal([]byte(out[start:]), &r); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out[start:])
	}
	return r
}

func TestAcceptanceCommand(t *testing.T) {
	code, out, errOut := runCLI(t,
		"demo", "--segments", "12", "--rotate-every", "3",
		"--license-ttl", "60s", "--advance-clock", "75s", "--replay-check")
	if code != exitOK {
		t.Fatalf("exit = %d, stderr:\n%s", code, errOut)
	}
	for _, want := range []string{"rotation_count=3", "segment_count=12", "ok_count=12", "failed_count=0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
	r := decodeJSON(t, out)
	if r.Verdict != "ok" || r.RotationCount != 3 || r.SegmentCount != 12 || r.FailedCount != 0 {
		t.Fatalf("unexpected report summary: %+v", r)
	}
	if len(r.Rotations) != 3 {
		t.Fatalf("%d rotations, want 3", len(r.Rotations))
	}
	if r.Rotations[0].ToKeyID != "k2" || r.Rotations[2].ToKeyID != "k4" {
		t.Fatalf("unexpected rotation key ids: %+v", r.Rotations)
	}
	if len(r.Segments) != 12 {
		t.Fatalf("%d segments, want 12", len(r.Segments))
	}
	for _, s := range r.Segments {
		if s.Status != "ok" || s.Reason != "NONE" {
			t.Fatalf("segment %d = %s/%s, want ok/NONE", s.SegmentIndex, s.Status, s.Reason)
		}
		if s.KeyID == "" {
			t.Fatalf("segment %d has no key id", s.SegmentIndex)
		}
	}
	if !r.Replay.Checked || !r.Replay.Rejected || r.Replay.Successes != 1 || r.Replay.Replays != 3 {
		t.Fatalf("unexpected replay result: %+v", r.Replay)
	}
	if !r.Boundary.Checked || r.Boundary.LicenseReason != "LICENSE_EXPIRED" {
		t.Fatalf("unexpected boundary result: %+v", r.Boundary)
	}
}

func TestCorruptSegmentExitTwo(t *testing.T) {
	code, out, _ := runCLI(t, "demo", "--segments", "12", "--rotate-every", "3", "--corrupt-segment", "5")
	if code != exitCheck {
		t.Fatalf("exit = %d, want %d", code, exitCheck)
	}
	r := decodeJSON(t, out)
	if r.FailedCount != 1 || r.OKCount != 11 {
		t.Fatalf("unexpected counts: %+v", r)
	}
	found := false
	for _, s := range r.Segments {
		if s.SegmentIndex == 5 {
			found = s.Reason == "AUTH_FAILED" && s.Status == "failed" && s.KeyID == "k2"
		}
	}
	if !found {
		t.Fatal("segment 5 was not reported as AUTH_FAILED")
	}
}

func TestDropSegmentExitTwo(t *testing.T) {
	code, out, _ := runCLI(t, "demo", "--segments", "12", "--rotate-every", "3", "--drop-segment", "8")
	if code != exitCheck {
		t.Fatalf("exit = %d, want %d", code, exitCheck)
	}
	r := decodeJSON(t, out)
	found := false
	for _, s := range r.Segments {
		if s.SegmentIndex == 8 {
			found = s.Reason == "SEGMENT_MISSING" && s.Status == "failed"
		}
	}
	if !found {
		t.Fatal("segment 8 was not reported as SEGMENT_MISSING")
	}
	if len(r.Segments) != 12 {
		t.Fatalf("%d segment results, want 12", len(r.Segments))
	}
}

func TestInvalidFlagExitOne(t *testing.T) {
	for _, args := range [][]string{
		{"demo", "--corrupt-segment", "99"},
		{"demo", "--segments", "0"},
		{"demo", "--segments", "10001"},
		{"demo", "--license-ttl", "-1s"},
		{"demo", "--rotate-every", "0"},
		{"demo", "--replay-attempts", "0"},
		{"unknown"},
		{},
	} {
		code, _, errOut := runCLI(t, args...)
		if code != exitError {
			t.Fatalf("%v: exit = %d, want %d", args, code, exitError)
		}
		if errOut == "" && len(args) > 0 && args[0] != "" {
			t.Fatalf("%v: no diagnostic on stderr", args)
		}
	}
}

func TestJSONOnlySuppressesSummary(t *testing.T) {
	code, out, _ := runCLI(t, "demo", "--segments", "4", "--json")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if strings.Contains(out, "summary asset_id") {
		t.Fatal("--json still printed the terminal summary")
	}
	if !strings.Contains(out, `"asset_id"`) {
		t.Fatal("--json did not print a report")
	}
}

func TestDeterministicOutput(t *testing.T) {
	args := []string{"demo", "--segments", "9", "--rotate-every", "2", "--advance-clock", "30s", "--replay-check"}
	_, first, _ := runCLI(t, args...)
	_, second, _ := runCLI(t, args...)
	if first != second {
		t.Fatal("seeded runs are not reproducible")
	}
}

func TestSingleSegment(t *testing.T) {
	code, out, _ := runCLI(t, "demo", "--segments", "1")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	r := decodeJSON(t, out)
	if r.RotationCount != 0 || r.SegmentCount != 1 || len(r.Rotations) != 0 {
		t.Fatalf("unexpected report: %+v", r)
	}
}

func TestRotationEverySegment(t *testing.T) {
	code, out, _ := runCLI(t, "demo", "--segments", "5", "--rotate-every", "1")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	r := decodeJSON(t, out)
	if r.RotationCount != 4 {
		t.Fatalf("rotation count = %d, want 4", r.RotationCount)
	}
	for i, s := range r.Segments {
		want := "k" + string(rune('1'+i))
		if s.KeyID != want {
			t.Fatalf("segment %d key = %s, want %s", i, s.KeyID, want)
		}
	}
}

func TestProductionModeUsesSystemEntropy(t *testing.T) {
	args := []string{"demo", "--segments", "5", "--production", "--json"}
	_, first, _ := runCLI(t, args...)
	_, second, _ := runCLI(t, args...)
	if first == second {
		t.Fatal("production mode produced identical output across runs")
	}
	r := decodeJSON(t, first)
	if r.Verdict != "ok" || r.FailedCount != 0 {
		t.Fatalf("production run failed: %+v", r)
	}
}
