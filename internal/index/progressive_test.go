package index_test

import (
	"path/filepath"
	"testing"

	"mediacore/internal/fixture"
	"mediacore/internal/index"
	"mediacore/internal/manifest"
)

func TestScanProgressiveMP4SampleTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progressive.mp4")
	specs := []fixture.SampleSpec{{Duration: 1800, Size: 20}, {Duration: 1800, Size: 28}}
	if err := fixture.WriteProgressiveMP4(path, 90000, specs); err != nil {
		t.Fatal(err)
	}
	e := manifest.Entry{
		StableKey:        "progressive:1",
		System:           "dash",
		Manifest:         path + ".mpd",
		Representation:   "prog",
		EntryIndex:       0,
		OriginalSequence: 1,
		MediaPath:        path,
		SourceTimescale:  90000,
		ProgramStartU:    0,
		ProgramEndU:      40_000_000,
		Aligned:          true,
	}
	idx, err := index.ScanEntry(e, index.DefaultScanOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Samples) != 2 {
		t.Fatalf("samples=%d", len(idx.Samples))
	}
	if idx.Samples[0].ByteOffset >= idx.Samples[1].ByteOffset {
		t.Fatalf("sample table offsets not ordered: %+v", idx.Samples)
	}
	r, err := index.QueryIndex(idx, 0, 40_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if r.SampleCount != 2 || r.FirstByteDeviation != 0 || r.LastByteDeviation != 0 {
		t.Fatalf("progressive report=%+v", r)
	}
}
