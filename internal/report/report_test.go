package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"streamcheck/internal/verify"
)

func sampleResults() []verify.TierResult {
	return []verify.TierResult{
		{
			Tier: "450k", BitrateBPS: 450000, Segments: 12,
			DurationMS: 36000, DeviationMS: 10,
		},
		{
			Tier: "900k", BitrateBPS: 900000, Segments: 12,
			DurationMS: 36000, DeviationMS: 33,
			Failures: []verify.Failure{
				{Code: verify.FailMissingSegment, Tier: "900k",
					Segment: "seg-00004.ts", Sequence: 4,
					Message: "segment file not found"},
			},
		},
	}
}

func TestFromResultsAggregation(t *testing.T) {
	r := FromResults(sampleResults())
	if r.ValidationFailures != 1 {
		t.Fatalf("failures: %d", r.ValidationFailures)
	}
	if r.MaxDeviationMS != 33 {
		t.Fatalf("max deviation: %d", r.MaxDeviationMS)
	}
	if len(r.Tiers) != 2 {
		t.Fatalf("tiers: %d", len(r.Tiers))
	}
}

func TestJSONRoundTrip(t *testing.T) {
	r := FromResults(sampleResults())
	var buf bytes.Buffer
	if err := r.JSON(&buf); err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if decoded.ValidationFailures != 1 || decoded.MaxDeviationMS != 33 {
		t.Fatalf("decoded: %+v", decoded)
	}
	if !strings.Contains(buf.String(), "validation_failures") {
		t.Fatal("JSON missing expected field")
	}
}

func TestTextOutputContainsKeyFields(t *testing.T) {
	r := FromResults(sampleResults())
	var buf bytes.Buffer
	r.Text(&buf)
	out := buf.String()
	for _, want := range []string{
		"bitrate_bps", "900000", "validation_failures=1",
		"result: FAIL", "missing_segment", "seg-00004.ts",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text output missing %q:\n%s", want, out)
		}
	}
}

func TestTextPassWhenClean(t *testing.T) {
	r := FromResults([]verify.TierResult{
		{Tier: "450k", Segments: 1},
	})
	var buf bytes.Buffer
	r.Text(&buf)
	if !strings.Contains(buf.String(), "result: PASS") {
		t.Fatalf("clean run must report PASS:\n%s", buf.String())
	}
	if r.ValidationFailures != 0 {
		t.Fatal("clean run must have zero failures")
	}
}
