package report

import (
	"strings"
	"testing"

	"archivecheck/internal/diagnostic"
	"archivecheck/internal/pipeline"
)

func TestBuildSortsMappingsAndStableDigest(t *testing.T) {
	results := []pipeline.Result{
		{Index: 2, Input: "c", Sample: 2, Status: pipeline.StatusSuccess, Outcome: pipeline.Outcome{Output: "out-c", ByteDelta: 0}},
		{Index: 0, Input: "a", Sample: 0, Status: pipeline.StatusSuccess, Outcome: pipeline.Outcome{Output: "out-a"}},
		{Index: 1, Input: "b", Sample: 1, Status: pipeline.StatusFailed, Diagnostic: &diagnostic.Error{
			Phase: diagnostic.PhaseSubtitle, Code: diagnostic.CodeTruncated, Sample: 1, Offset: 42, Message: "cut",
		}},
	}
	a := Build(17, 1<<20, results)
	b := Build(17, 1<<20, []pipeline.Result{results[2], results[0], results[1]})
	if a.StableDigest != b.StableDigest {
		t.Fatalf("digests differ for reordered completion: %s vs %s", a.StableDigest, b.StableDigest)
	}
	if len(a.Mappings) != 3 || a.Mappings[0].Input != "a" || a.Mappings[2].Input != "c" {
		t.Fatalf("mapping order = %+v", a.Mappings)
	}
	if a.Counts.Success != 2 || a.Counts.Failed != 1 {
		t.Fatalf("counts = %+v", a.Counts)
	}
	text := a.FormatTerminal()
	for _, want := range []string{"input=a output=out-a", "time_delta_ns=0", "byte_delta=0", "phase=subtitle code=TRUNCATED_INPUT sample=1 offset=42", "stable_digest="} {
		if !strings.Contains(text, want) {
			t.Fatalf("terminal output missing %q:\n%s", want, text)
		}
	}
}

func TestCanonicalJSONContainsNoRuntimeOrderField(t *testing.T) {
	s := Build(17, 100, []pipeline.Result{{Index: 0, Status: pipeline.StatusSuccess, Outcome: pipeline.Outcome{OutputByteCount: 7}}})
	data, err := s.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(data)
	if strings.Contains(jsonText, "duration") || strings.Contains(jsonText, "workers") || strings.Contains(jsonText, "completion_order") {
		t.Fatalf("summary contains runtime/order data:\n%s", jsonText)
	}
	if !strings.Contains(jsonText, `"stable_digest": ""`) {
		t.Fatal("canonical JSON should omit the computed digest field value for independent hashing")
	}
}
