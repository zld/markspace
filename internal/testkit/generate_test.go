package testkit

import (
	"path/filepath"
	"testing"

	"mediacore/internal/scan"
)

func TestGenerateAndScanRoundTrip(t *testing.T) {
	dir := t.TempDir()
	spec := MP4Spec{
		Path: filepath.Join(dir, "t.mp4"), Manifest: filepath.Join(dir, "m.json"),
		TrackID: 1, Timescale: 90000, SampleCount: 300, SampleDelta: 3000,
		KeyEvery: 30, MediaStartTicks: 90000, TargetBPS: 1_800_000,
		JitterPct: 5, Seed: 42,
	}
	if err := WriteMP4(spec); err != nil {
		t.Fatal(err)
	}
	m, err := WriteManifest(spec)
	if err != nil {
		t.Fatal(err)
	}
	f, tracks, err := scan.OpenFile(spec.Path, m, scan.DefaultConfig())
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	defer f.Close()
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d", len(tracks))
	}
	tr := tracks[0]
	if tr.Count != 300 || tr.SyncCount != 10 {
		t.Fatalf("count=%d sync=%d", tr.Count, tr.SyncCount)
	}
	if tr.Edits.MediaOrigin <= 0 || len(tr.Edits.Edits) != 1 {
		t.Fatalf("edits = %+v", tr.Edits)
	}
	var seen int
	var prev int64 = -1
	err = tr.ScanBatches(scan.DefaultConfig(), func(batch []scan.Sample) error {
		for _, s := range batch {
			if s.DTS <= prev && seen > 0 {
				t.Fatalf("non-increasing DTS at %d", seen)
			}
			prev = s.DTS
			if s.Offset == 0 || s.Size != 8 {
				t.Fatalf("bad sample %+v", s)
			}
			seen++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if seen != 300 {
		t.Fatalf("walked %d samples", seen)
	}
}
