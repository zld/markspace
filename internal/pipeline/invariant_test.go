package pipeline_test

import (
	"testing"

	"archivectl/internal/fixture"
	"archivectl/internal/pipeline"
	"archivectl/internal/timeline"
)

func runOnce(t *testing.T, seed int64) *pipeline.Result {
	t.Helper()
	set := fixture.Generate(seed, 8)
	res, err := pipeline.Run(set, pipeline.DefaultOptions())
	if err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	return res
}

// Truncation and loss must be verified across parse, merge and export, not
// just at the container boundary.
func TestTruncationFlowsAcrossStages(t *testing.T) {
	res := runOnce(t, 42)

	// Parse stage: the truncated document produced a structured error.
	var sawTruncation bool
	for _, e := range res.Errors {
		if e.Stage == "ebml/truncated" {
			sawTruncation = true
		}
	}
	if !sawTruncation {
		t.Fatal("truncation not surfaced as a structured parse error")
	}

	// Recover stage: index rebuilt, damaged offsets recorded.
	if !res.Index.Rebuilt || !res.Index.Truncated {
		t.Fatal("index must be rebuilt and flagged truncated")
	}
	if len(res.Index.DamagedBytes) == 0 {
		t.Fatal("damaged offsets missing from rebuilt index")
	}

	// Merge stage: every retained sample references a valid track and stays
	// inside the observed source range.
	for _, s := range res.Merged.Samples {
		if !res.Graph.Has(s.TrackNumber) {
			t.Fatalf("merged sample references invalid track %d", s.TrackNumber)
		}
		if !res.Merged.Clock.InRange(s.StartNs) ||
			!res.Merged.Clock.InRange(s.EndNs()) {
			t.Fatalf("sample %d out of source range", s.StartNs)
		}
	}

	// Export stage: frames were produced and fully hashed.
	if len(res.Frames) == 0 || len(res.FrameHashes) != len(res.Frames) {
		t.Fatal("export must produce one hash per frame")
	}
	if res.ManifestSHA == "" {
		t.Fatal("manifest digest missing")
	}
}

func TestLossRecoveredAcrossStages(t *testing.T) {
	res := runOnce(t, 42)
	if res.RTP == nil {
		t.Fatal("RTP recovery result missing")
	}
	if res.RTP.Expected == 0 || res.RTP.Unrecoverable == 0 {
		t.Fatal("synthetic capture must contain planned packet loss")
	}
	if rate := res.RTP.RecoveryRate(); rate < 0.90 {
		t.Fatalf("in-window recovery rate %f below 0.90", rate)
	}
	// Ordered output is strictly increasing across sequence wrap space.
	for i := 1; i < len(res.RTP.Ordered); i++ {
		prev := res.RTP.Ordered[i-1].SequenceNumber
		cur := res.RTP.Ordered[i].SequenceNumber
		if int16(cur-prev) <= 0 {
			t.Fatal("recovered RTP output not strictly increasing")
		}
	}
}

func TestTimebaseIsIntegerNanosecondsAndMonotonic(t *testing.T) {
	res := runOnce(t, 42)
	last := int64(0)
	for i, s := range res.Merged.Samples {
		if s.StartNs < 0 {
			t.Fatal("negative nanosecond timestamp")
		}
		if i > 0 && s.StartNs < last {
			t.Fatal("merged timeline not globally monotonic")
		}
		last = s.StartNs
	}
	// Frame timestamps must stay within the source clock range.
	for _, f := range res.Frames {
		if !res.Merged.Clock.InRange(f.TimeNs) {
			t.Fatalf("frame at %d outside source range", f.TimeNs)
		}
	}
}

func TestErrorCategoriesAreStructured(t *testing.T) {
	res := runOnce(t, 42)
	known := map[string]bool{
		timeline.CategoryInvalidTrack: true,
		timeline.CategoryNonMonotonic: true,
		timeline.CategoryOutOfRange:   true,
		timeline.CategoryDuplicate:    true,
		"truncated_input":             true,
		"length_out_of_range":         true,
		"malformed_block":             true,
		"malformed_vint":              true,
	}
	for _, e := range res.Errors {
		if e.Stage == "" || e.Category == "" {
			t.Fatalf("error missing stage/category: %+v", e)
		}
		if !known[e.Category] {
			t.Fatalf("unclassified error category %q", e.Category)
		}
	}
}

func TestDeterministicRerunHashesAndCounts(t *testing.T) {
	first := runOnce(t, 42)
	second := runOnce(t, 42)
	if first.ManifestSHA != second.ManifestSHA {
		t.Fatal("manifest digest differs across identical runs")
	}
	if len(first.FrameHashes) != len(second.FrameHashes) {
		t.Fatal("frame count differs across identical runs")
	}
	for i := range first.FrameHashes {
		if first.FrameHashes[i].SHA256 != second.FrameHashes[i].SHA256 {
			t.Fatalf("frame %d hash differs across identical runs", i)
		}
	}
	if first.SampleCount != second.SampleCount ||
		len(first.Errors) != len(second.Errors) ||
		first.RTP.Recovered != second.RTP.Recovered ||
		first.RTP.Unrecoverable != second.RTP.Unrecoverable {
		t.Fatal("counts differ across identical runs")
	}
}

func TestAVSyncP95WithinTolerance(t *testing.T) {
	res := runOnce(t, 42)
	if res.AVSyncP95Ms > 20.0 {
		t.Fatalf("av_sync P95 %fms exceeds 20ms", res.AVSyncP95Ms)
	}
}

func TestInvalidLossWindowRejected(t *testing.T) {
	set := fixture.Generate(42, 8)
	opts := pipeline.DefaultOptions()
	opts.LossWindow = pipeline.MaxLossWindow + 1
	if _, err := pipeline.Run(set, opts); err == nil {
		t.Fatal("over-cap loss window must be rejected")
	}
}

func TestOversizedInputRejected(t *testing.T) {
	if pipeline.MaxFileBytes != int64(2)<<30 {
		t.Fatalf("file cap must be 2 GiB, got %d", pipeline.MaxFileBytes)
	}
	if err := pipeline.EnforceFileSize("normal", pipeline.MaxFileBytes); err != nil {
		t.Fatalf("a file exactly at the cap must pass, got %v", err)
	}
	err := pipeline.EnforceFileSize("normal", pipeline.MaxFileBytes+1)
	if err == nil {
		t.Fatal("a file above the cap must be rejected")
	}
	if se, ok := err.(pipeline.StageError); !ok || se.Category != "file_too_large" {
		t.Fatalf("want file_too_large stage error, got %v", err)
	}
}
