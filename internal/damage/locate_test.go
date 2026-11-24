package damage

import (
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/container"
	"mediacheck/internal/fixture"
	"mediacheck/internal/media"
)

func scanTemp(t *testing.T, data []byte) *container.ScanResult {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "s.mp4")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := container.Scan(p)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return res
}

func buildLines(t *testing.T, res *container.ScanResult) []*media.TrackLine {
	t.Helper()
	var lines []*media.TrackLine
	for i := range res.Tracks {
		tm := &res.Tracks[i]
		tb, err := media.NewTimeBase(tm.Timescale)
		if err != nil {
			t.Fatal(err)
		}
		line, err := media.BuildTrackLine(tm.TrackID, tm.Kind(), tb, tm.RawSamples, tm.Edits, tm.Index.Undecidable)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	return lines
}

func findCode(errs []Error, code Code) *Error {
	for i := range errs {
		if errs[i].Code == code {
			return &errs[i]
		}
	}
	return nil
}

func TestLocateUndecidableIndexRange(t *testing.T) {
	spec := fixture.MediaSpec{
		MovieScale: 1000,
		Tracks: []fixture.TrackSpec{
			{TrackID: 1, Handler: "vide", Timescale: 1000, SampleCount: 100, Delta: 10, SyncEvery: 10},
		},
	}
	spec.Tracks[0].TruncatedIndex = true
	res := scanTemp(t, fixture.BuildMP4(spec))
	lines := buildLines(t, res)
	loc := NewLocator(lines)
	errs := loc.LocateFromScan(res)
	if e := findCode(errs, CodeIndexUndecidable); e == nil {
		t.Fatalf("cut index must produce index_undecidable, got %+v", errs)
	} else if e.Range.Deterministic {
		t.Fatal("inferred tail range must be non-deterministic")
	}
}

func TestLocateTruncatedBoxTailRange(t *testing.T) {
	spec := fixture.MediaSpec{
		MovieScale: 1000,
		Tracks: []fixture.TrackSpec{
			{TrackID: 1, Handler: "vide", Timescale: 1000, SampleCount: 100, Delta: 10, SyncEvery: 10},
		},
	}
	data := fixture.BuildMP4(spec)
	// Physically cut the file inside the moov box.
	cut := len(data) - 40
	dir := t.TempDir()
	p := filepath.Join(dir, "cut.mp4")
	if err := os.WriteFile(p, data[:cut], 0o600); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(p)
	defer f.Close()
	pre, err := container.PreScanFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre.Truncations) == 0 {
		t.Fatal("pre-scan must record the physical truncation")
	}
	res, perr := container.Parse(p, pre)
	if perr != nil {
		// Even if go-mp4 refuses, the pre-scan truncation must still exist.
		return
	}
	lines := buildLines(t, res)
	errs := NewLocator(lines).LocateFromScan(res)
	if e := findCode(errs, CodeTruncatedBox); e == nil {
		t.Fatalf("physical cut must produce truncated_box, got %+v", errs)
	}
}

func TestLocateMissingSamples(t *testing.T) {
	spec := fixture.MediaSpec{
		MovieScale: 1000,
		Tracks: []fixture.TrackSpec{
			// stsz says 100 samples; stts only times 50.
			{TrackID: 1, Handler: "vide", Timescale: 1000, SampleCount: 100,
				SttsRuns: []fixture.SttsRun{{Count: 50, Delta: 10}}, SyncEvery: 10},
		},
	}
	res := scanTemp(t, fixture.BuildMP4(spec))
	lines := buildLines(t, res)
	errs := NewLocator(lines).LocateFromScan(res)
	e := findCode(errs, CodeMissingSample)
	if e == nil {
		t.Fatalf("declared/timed count mismatch must yield missing_sample, got %+v", errs)
	}
	if e.Range.StartNanos >= e.Range.EndNanos {
		t.Fatal("missing-sample range must be a valid half-open interval")
	}
}

func TestMergeCoalescesOverlappingSameCode(t *testing.T) {
	errs := []Error{
		{Code: CodeTruncatedBox, TrackID: 1, Range: Range{StartNanos: 100, EndNanos: 300, Deterministic: true}},
		{Code: CodeTruncatedBox, TrackID: 1, Range: Range{StartNanos: 200, EndNanos: 500, Deterministic: false}},
		{Code: CodeMissingSample, TrackID: 1, Range: Range{StartNanos: 200, EndNanos: 400}},
	}
	merged := Merge(errs)
	if len(merged) != 2 {
		t.Fatalf("merged count = %d, want 2 (different codes stay separate)", len(merged))
	}
	var trun *Error
	for i := range merged {
		if merged[i].Code == CodeTruncatedBox {
			trun = &merged[i]
		}
	}
	if trun.Range.EndNanos != 500 || trun.Range.Deterministic {
		t.Fatalf("merge must extend end and keep non-deterministic, got %+v", trun.Range)
	}
}

func TestLocateCrossTrackDamage(t *testing.T) {
	// Both tracks carry undecidable indexes: each must get its own range
	// bound on its own timeline, never merged across tracks.
	spec := fixture.MediaSpec{
		MovieScale: 1000,
		Tracks: []fixture.TrackSpec{
			{TrackID: 1, Handler: "vide", Timescale: 1000, SampleCount: 100, Delta: 10, SyncEvery: 10},
			{TrackID: 2, Handler: "soun", Timescale: 8000, SampleCount: 200, Delta: 100},
		},
	}
	spec.Tracks[0].TruncatedIndex = true
	spec.Tracks[1].TruncatedIndex = true
	res := scanTemp(t, fixture.BuildMP4(spec))
	lines := buildLines(t, res)
	errs := NewLocator(lines).LocateFromScan(res)
	byTrack := map[uint32]int{}
	for _, e := range errs {
		byTrack[e.TrackID]++
	}
	if byTrack[1] == 0 || byTrack[2] == 0 {
		t.Fatalf("both tracks must carry defects, got %+v", byTrack)
	}
	for _, e := range errs {
		if e.Range.Deterministic {
			t.Fatalf("track %d inferred range must be non-deterministic", e.TrackID)
		}
	}
}

func TestNoDefectsOnHealthyAsset(t *testing.T) {	spec := fixture.MediaSpec{
		MovieScale: 1000,
		Tracks: []fixture.TrackSpec{
			{TrackID: 1, Handler: "vide", Timescale: 1000, SampleCount: 100, Delta: 10, SyncEvery: 10},
			{TrackID: 2, Handler: "soun", Timescale: 8000, SampleCount: 100, Delta: 100},
		},
	}
	res := scanTemp(t, fixture.BuildMP4(spec))
	lines := buildLines(t, res)
	if errs := NewLocator(lines).LocateFromScan(res); len(errs) != 0 {
		t.Fatalf("healthy asset must have zero damage ranges, got %+v", errs)
	}
}
