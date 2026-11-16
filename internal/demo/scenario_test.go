package demo

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"jitter/internal/metrics"
)

func acceptanceParams() Params {
	return Params{
		Path: "both", Seed: 41, Frames: 600,
		Loss: 0.04, Reorder: 0.08, Duplicate: 0.02,
		WaterlineEveryMs: 200,
	}
}

func runToBuffer(t *testing.T, p Params) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Run(p, &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return buf.String()
}

func parseSummaries(t *testing.T, out string) []metrics.Summary {
	t.Helper()
	var sums []metrics.Summary
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var s metrics.Summary
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatalf("bad summary line %q: %v", line, err)
		}
		sums = append(sums, s)
	}
	return sums
}

func TestDemoAcceptance(t *testing.T) {
	out := runToBuffer(t, acceptanceParams())
	if !strings.HasPrefix(out, "path,elapsed_ms,queued_frames,target_frames\n") {
		t.Fatal("missing CSV header")
	}
	sums := parseSummaries(t, out)
	if len(sums) != 2 {
		t.Fatalf("summaries = %d, want 2 (udp and trace)", len(sums))
	}
	for _, s := range sums {
		if s.FramesOut != 600 {
			t.Errorf("%s: frames_out = %d, want 600", s.Path, s.FramesOut)
		}
		if s.ConcealedFrames < 10 || s.ConcealedFrames > 100 {
			t.Errorf("%s: concealed_frames = %d, want 10..100", s.Path, s.ConcealedFrames)
		}
		if s.ContinuityOKRatio < 0.95 {
			t.Errorf("%s: continuity_ok_ratio = %v, want >= 0.95", s.Path, s.ContinuityOKRatio)
		}
		if s.DropsDuplicate == 0 {
			t.Errorf("%s: expected duplicate drops", s.Path)
		}
	}
}

func TestDemoReproducible(t *testing.T) {
	a := runToBuffer(t, acceptanceParams())
	b := runToBuffer(t, acceptanceParams())
	if a != b {
		t.Fatal("same seed produced different output")
	}
	other := acceptanceParams()
	other.Seed = 42
	if c := runToBuffer(t, other); c == a {
		t.Fatal("different seed produced identical output")
	}
}

func TestDemoCSVOutFile(t *testing.T) {
	p := acceptanceParams()
	p.Path = "trace"
	p.Frames = 100
	p.CSVOut = t.TempDir() + "/waterline.csv"
	var stdout bytes.Buffer
	if err := Run(p, &stdout); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(stdout.String(), "elapsed_ms") {
		t.Fatal("CSV must go to the file, not stdout")
	}
	if !strings.Contains(stdout.String(), `"frames_out":100`) {
		t.Fatalf("summary missing from stdout: %s", stdout.String())
	}
}

func TestValidate(t *testing.T) {
	good := acceptanceParams()
	if err := good.Validate(); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
	bad := []Params{
		{Path: "nope", Frames: 1, WaterlineEveryMs: 200},
		{Path: "both", Frames: 0, WaterlineEveryMs: 200},
		{Path: "both", Frames: 1, Loss: 1.5, WaterlineEveryMs: 200},
		{Path: "both", Frames: 1, Reorder: -0.1, WaterlineEveryMs: 200},
		{Path: "both", Frames: 1, WaterlineEveryMs: 0},
	}
	for i, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("bad params %d accepted", i)
		}
	}
}
