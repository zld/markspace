package mp4_test

import (
	"path/filepath"
	"testing"

	"medpipe/internal/apperr"
	"medpipe/internal/clock"
	"medpipe/internal/fixture"
	"medpipe/internal/mp4"
)

func generateFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := fixture.Generate(fixture.Options{Dir: dir, Seed: 7}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScanGeneratedSampleTables(t *testing.T) {
	dir := generateFixtures(t)
	var samples []mp4.Sample
	scan, err := mp4.ScanFile("wrap", filepath.Join(dir, "wrap.mp4"), 4, func(batch []mp4.Sample) error {
		samples = append(samples, batch...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scan.SampleCount != 6 || len(samples) != 6 {
		t.Fatalf("counts scan=%d expanded=%d", scan.SampleCount, len(samples))
	}
	if samples[0].DTS != 0 || samples[5].DTS != 15000 {
		t.Fatalf("DTS expansion wrong: first=%d last=%d", samples[0].DTS, samples[5].DTS)
	}
	if !samples[0].Sync || !samples[3].Sync || samples[1].Sync {
		t.Fatalf("sync flags wrong: %#v", scan.SyncSamples)
	}

	var editSamples []mp4.Sample
	editScan, err := mp4.ScanFile("edit", filepath.Join(dir, "edit.mp4"), 4, func(b []mp4.Sample) error {
		editSamples = append(editSamples, b...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(editSamples) != 6 || len(editScan.Edits) != 2 {
		t.Fatalf("edit samples=%d edits=%#v", len(editSamples), editScan.Edits)
	}
	if !editScan.Edits[0].Empty || !editScan.Edits[0].Duration.Equal(clock.NewRational(1, 10)) {
		t.Fatalf("empty edit not preserved exactly: %#v", editScan.Edits[0])
	}
}

func TestScanBadRangeReportsSampleAndByteOffset(t *testing.T) {
	dir := generateFixtures(t)
	_, err := mp4.ScanFile("range-bad", filepath.Join(dir, "bad-range.mp4"), 4, nil)
	pe, ok := apperr.As(err)
	if !ok || pe.Code != "E_SAMPLE_RANGE" {
		t.Fatalf("err=%v pe=%#v", err, pe)
	}
	if pe.Sample != 2 || pe.ByteOffset <= 0 {
		t.Fatalf("location sample=%d offset=%d", pe.Sample, pe.ByteOffset)
	}
}
