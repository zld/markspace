package evidence

import (
	"encoding/json"
	"testing"

	"rtptrace/internal/recover"
	"rtptrace/internal/segment"
)

func testInput() Input {
	return Input{
		Seed: 17, ExitCode: 0, RecoveryItems: []recover.Item{{Code: "none", RecoveryState: recover.StateOK}},
		Alignment: &segment.AlignedReport{
			SegmentCount: 1,
			Mappings: []segment.Mapping{{
				InputSample: "track-1/sample-1", TrackID: 1, PresentationStart: "0",
				SegmentID: "seg-000", Representation: "rep-800k",
				TimeErrorTicks: 0, ByteError: 0,
			}},
		},
	}
}

func TestBuildStableSummaryAndRequiredKeys(t *testing.T) {
	r1 := Build(testInput())
	r2 := Build(testInput())
	if r1.SummarySHA256 != r2.SummarySHA256 || len(r1.SummarySHA256) != 64 {
		t.Fatalf("summaries unstable or bad length: %q %q", r1.SummarySHA256, r2.SummarySHA256)
	}
	if r1.Mappings[0].SummarySHA256 != r1.SummarySHA256 {
		t.Fatal("row summary does not match report")
	}
	data, err := r1.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]interface{}
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	required := []string{
		"input_sample", "track_id", "presentation_start", "segment_id", "representation",
		"time_error_ticks", "byte_error", "failure_track", "failure_sample",
		"recovery_state", "summary_sha256",
	}
	for _, k := range required {
		if _, ok := top[k]; !ok {
			t.Fatalf("top-level JSON missing %s", k)
		}
	}
	rows := top["mappings"].([]interface{})
	row := rows[0].(map[string]interface{})
	for _, k := range required {
		if _, ok := row[k]; !ok {
			t.Fatalf("mapping JSON missing %s", k)
		}
	}
}

func TestSummaryChangesWhenNormalizedRowChanges(t *testing.T) {
	a := Build(testInput())
	in := testInput()
	in.Alignment.Mappings[0].ByteError = 42
	b := Build(in)
	if a.SummarySHA256 == b.SummarySHA256 {
		t.Fatal("summary unexpectedly unchanged after normalized field change")
	}
}
