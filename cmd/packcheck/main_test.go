package main

import (
	"strings"
	"testing"
)

func TestNormalScenarioOutputs(t *testing.T) {
	out := captureRun(t, "--self-generate", "--fps", "30000/1001-df",
		"--slow-layer", "low", "--queue", "2", "--budget-bytes", "0")
	for _, want := range []string{
		"layer_count=3", "aligned_boundaries=true", "threshold=pass",
		"exit=0", "peak_queue_depth=2", "in_manifest=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("normal output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "in_manifest=false") {
		t.Error("healthy layers must all enter the manifest")
	}
}

func TestQueuePeakNeverExceedsCapacity(t *testing.T) {
	out := captureRun(t, "--self-generate", "--queue", "2", "--slow-layer", "low")
	if !strings.Contains(out, "peak_queue_depth=2") {
		t.Errorf("peak queue depth not capped at 2:\n%s", out)
	}
	if strings.Contains(out, "peak_queue_depth=3") {
		t.Error("queue peak exceeded the declared capacity")
	}
}

func TestSlowConsumerIsolationKeepsOthersMoving(t *testing.T) {
	out := captureRun(t, "--self-generate", "--slow-layer", "low", "--queue", "2")
	if !strings.Contains(out, "anomaly representation=low code=slow-consumer") {
		t.Errorf("slow-layer isolation not recorded:\n%s", out)
	}
	// The fast layers must remain healthy and manifest-eligible.
	for _, name := range []string{"mid", "high"} {
		needle := "layer name=" + name + " container=pass subtitle=pass boundary=pass verdict=passed in_manifest=true"
		if !strings.Contains(out, needle) {
			t.Errorf("layer %s blocked by slow consumer:\n%s", name, out)
		}
	}
}

func TestCorruptScenarioExcludesFailedLayerAndFailsThreshold(t *testing.T) {
	out := captureRun(t, "--self-generate", "--corrupt", "subtitle-offset",
		"--truncate", "layer=mid", "--split-at-cue", "--budget-bytes", "0")
	for _, want := range []string{
		"layer name=mid container=fail", "verdict=recovered in_manifest=false",
		"aligned_boundaries=false", "threshold=fail", "exit=2",
		"anomaly representation=subtitle code=cue-at-cutpoint",
		"crosscheck representation=mid", "budget_delta_bytes=13",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("corrupt output missing %q:\n%s", want, out)
		}
	}
	// The manifest must list only the two intact layers.
	if strings.Contains(out, "entry mid") {
		t.Error("failed mid layer must not enter the rollback manifest")
	}
}

func TestManifestDigestDeterministic(t *testing.T) {
	first := captureRun(t, "--self-generate")
	second := captureRun(t, "--self-generate")
	d1 := extractDigest(first)
	d2 := extractDigest(second)
	if d1 == "" || d1 != d2 {
		t.Errorf("manifest digest not deterministic: %q vs %q", d1, d2)
	}
}

func TestBadArgumentsExitOne(t *testing.T) {
	if code := run([]string{"--unknown"}); code != 1 {
		t.Errorf("unknown flag exit=%d want 1", code)
	}
	if code := run([]string{"--self-generate", "--fps", "30"}); code != 1 {
		t.Errorf("bad fps exit=%d want 1", code)
	}
	if code := run([]string{}); code != 1 {
		t.Errorf("missing --self-generate exit=%d want 1", code)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	if code := run([]string{"--self-generate", "--out", "../escape"}); code != 1 {
		t.Errorf("path traversal exit=%d want 1", code)
	}
}

func captureRun(t *testing.T, args ...string) string {
	t.Helper()
	_, out := runCaptured(args...)
	return out
}

func extractDigest(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "manifest_digest=") {
			return strings.TrimPrefix(line, "manifest_digest=")
		}
	}
	return ""
}
