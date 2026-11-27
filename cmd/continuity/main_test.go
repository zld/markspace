package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func demo(t *testing.T, args ...string) (int, output, errOutput) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"demo"}, args...), &stdout, &stderr)
	var out output
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
		}
	}
	var eo errOutput
	if stderr.Len() > 0 && strings.HasPrefix(strings.TrimSpace(stderr.String()), "{") {
		json.Unmarshal(stderr.Bytes(), &eo)
	}
	return code, out, eo
}

func TestDemoValid(t *testing.T) {
	code, out, _ := demo(t, "--case", "valid", "--representations", "2", "--segment", "2s")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out.RoundtripRate != 1.0 || out.KeyframeHitRate != 1.0 || !out.Verified {
		t.Fatalf("metrics %+v", out)
	}
	if out.AlignedBoundaryDeltaNs > 0 {
		t.Fatalf("boundary delta %d", out.AlignedBoundaryDeltaNs)
	}
	if len(out.SegmentHashes) != 6 {
		t.Fatalf("got %d segment hashes", len(out.SegmentHashes))
	}
	for _, h := range out.SegmentHashes {
		if len(h) != 64 {
			t.Fatalf("hash %q not 64 hex chars", h)
		}
	}
	// Segment hashes must be reproducible across independent runs.
	_, out2, _ := demo(t, "--case", "valid", "--representations", "2", "--segment", "2s")
	for i := range out.SegmentHashes {
		if out.SegmentHashes[i] != out2.SegmentHashes[i] {
			t.Fatalf("segment %d hash not reproducible", i)
		}
	}
}

func TestDemoCorruptBox(t *testing.T) {
	code, _, eo := demo(t, "--case", "corrupt-box")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if eo.Kind != "truncated-box" || eo.Offset == nil || *eo.Offset <= 0 {
		t.Fatalf("error output %+v", eo)
	}
}

func TestDemoMissingKeyframe(t *testing.T) {
	code, _, eo := demo(t, "--case", "missing-keyframe")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if eo.Kind != "keyframe-miss" {
		t.Fatalf("error output %+v", eo)
	}
}

func TestDemoResume(t *testing.T) {
	code, out, _ := demo(t, "--case", "resume")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out.ReusedSegments == 0 || out.RecomputedSegments != 1 {
		t.Fatalf("reused=%d recomputed=%d", out.ReusedSegments, out.RecomputedSegments)
	}
	if out.ManifestStable == nil || !*out.ManifestStable {
		t.Fatal("manifest hash must be stable across resume")
	}
}

func TestDemoBudgetError(t *testing.T) {
	code, _, _ := demo(t, "--case", "valid", "--max-buffer", "1024")
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestDemoBadParams(t *testing.T) {
	code, _, _ := demo(t, "--case", "valid", "--representations", "99")
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}
