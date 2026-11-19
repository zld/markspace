package report

import (
	"encoding/json"
	"testing"

	"mediakey/internal/manifest"
)

func TestBuildSortsAndCounts(t *testing.T) {
	results := []manifest.ChunkResult{
		{ChunkIndex: 2, KeyID: "key-r001", Status: manifest.StatusFailed, FailureReason: manifest.ReasonReplay},
		{ChunkIndex: 0, KeyID: "key-r000", Status: manifest.StatusOK},
		{ChunkIndex: 1, KeyID: "key-r000", Status: manifest.StatusFailed, FailureReason: manifest.ReasonAuthFailed},
	}
	r := Build("asset-x", 1, results)
	if r.ReportVersion == "" {
		t.Fatal("missing report version")
	}
	if r.ChunkResults[0].ChunkIndex != 0 || r.ChunkResults[2].ChunkIndex != 2 {
		t.Fatal("results not sorted by chunk index")
	}
	if r.Summary.Total != 3 || r.Summary.OK != 1 || r.Summary.Failed != 2 {
		t.Fatalf("summary counts wrong: %+v", r.Summary)
	}
	if r.Summary.Reasons[string(manifest.ReasonReplay)] != 1 ||
		r.Summary.Reasons[string(manifest.ReasonAuthFailed)] != 1 {
		t.Fatalf("reason counts wrong: %+v", r.Summary.Reasons)
	}
	raw, err := MarshalJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if decoded["rotation_count"].(float64) != 1 {
		t.Fatal("rotation_count missing or wrong in JSON")
	}
}
