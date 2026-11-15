package report

import (
	"bytes"
	"strings"
	"testing"
)

func TestHasConsistencyFailureOnlyForMarkedFindings(t *testing.T) {
	r := &Report{}
	r.AddFinding(Finding{Code: CodeMissingPart, Bitrate: 1, Seq: 3})
	if r.HasConsistencyFailure() {
		t.Fatal("unmarked finding must not drive exit code 2")
	}
	r.MarkConsistency()
	if !r.HasConsistencyFailure() {
		t.Fatal("marked finding must drive exit code 2")
	}
}

func TestWriteTextIncludesRequiredCounters(t *testing.T) {
	r := &Report{
		Command: "verify",
		Tracks: []TrackSummary{{
			BitrateKbps: 800, Fragments: 24, Segments: 6, Parts: 24,
			DurationS: 12.0, DeviationMs: 80, Failures: 1,
		}},
		Findings: []Finding{{
			Code: CodeTimelineDrift, Bitrate: 800000,
			Detail: "drift", Consistency: true,
		}},
	}
	var buf bytes.Buffer
	WriteText(&buf, r)
	out := buf.String()
	for _, want := range []string{
		"bitrate_kbps=800", "fragments=24", "segments=6", "parts=24",
		"duration_s=12.0", "deviation_ms=80", "failures=1",
		"TIMELINE_DRIFT", "bitrate=800000",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteTextNoFailures(t *testing.T) {
	var buf bytes.Buffer
	WriteText(&buf, &Report{Command: "demo"})
	if !strings.Contains(buf.String(), "failures: none") {
		t.Fatalf("want explicit none marker, got %q", buf.String())
	}
}

func TestWriteJSONRoundTrips(t *testing.T) {
	r := &Report{
		Command: "build",
		Tracks:  []TrackSummary{{BitrateKbps: 1600, Fragments: 23}},
		Findings: []Finding{{
			Code: CodeMissingPart, Bitrate: 1600000, Seq: 13,
			Consistency: true,
		}},
	}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "\"command\": \"build\"") ||
		!strings.Contains(out, "\"code\": \"MISSING_PART\"") ||
		!strings.Contains(out, "\"consistency\": true") {
		t.Fatalf("JSON missing expected fields:\n%s", out)
	}
}
