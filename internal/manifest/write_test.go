package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediamerge/internal/audio"
	"mediamerge/internal/fingerprint"
	"mediamerge/internal/model"
	"mediamerge/internal/segment"
	"mediamerge/internal/track"
)

func testRun(name string, exit int) RunInput {
	samples := []model.Sample{
		{TrackID: 2, SeqNum: 1, SourceFile: "a.mp4", Start: 0, Dur: 1_000_000_000, Size: 1000, Index: 0},
		{TrackID: 2, SeqNum: 2, SourceFile: "a.mp4", Start: 1_000_000_000, Dur: 1_000_000_000, Size: 1000, Index: 1},
	}
	seg := segment.Split(samples, segment.Options{MaxDuration: 1_500_000_000})
	return RunInput{
		Name:     name,
		ExitCode: exit,
		Segments: seg,
		Audio: []audio.Report{{TrackID: 2,
			Intervals: []model.Interval{{Start: 0, End: 500_000_000}},
			Missing:   []model.Interval{{Start: 2_000_000_000, End: 2_500_000_000}},
			Stats:     audio.Stats{PCMParts: 48000}}},
		Fingerprints: fingerprint.Report{},
		Tracks: &track.Normalized{Tracks: []track.Metadata{
			{ID: 2, Handler: "soun", Source: "a.mp4", Timescale: 48000,
				SampleRate: 48000, Channels: 2, Layout: "stereo", Mixable: true},
		}},
	}
}

func TestBuildDocumentAggregates(t *testing.T) {
	doc := Build(Input{Seed: 17, SegmentMs: 2000, TargetBps: 640000,
		Runs: []RunInput{testRun("normal", 0), testRun("damaged", 2)}})
	if doc.Segments != 4 {
		t.Fatalf("segments=%d", doc.Segments)
	}
	if doc.SilenceMs != 1000 {
		t.Fatalf("silence=%d", doc.SilenceMs)
	}
	if doc.QualitySamples != 96000 {
		t.Fatalf("quality=%d", doc.QualitySamples)
	}
	if doc.TargetBitrateBps != 640000 {
		t.Fatalf("target=%d", doc.TargetBitrateBps)
	}
	// 4000 bytes over 4 s = 8000 bps.
	if doc.ActualBitrateBps != 8000 {
		t.Fatalf("actual=%d", doc.ActualBitrateBps)
	}
	wantDev := round2((float64(8000) - 640000) / 640000 * 100)
	if doc.BitrateDeviationPct != wantDev {
		t.Fatalf("dev=%v want %v", doc.BitrateDeviationPct, wantDev)
	}
	if !doc.BitrateEstimated || doc.Note == "" {
		t.Fatal("estimate disclaimer missing")
	}
	if len(doc.Paths) != 2 || doc.Paths[1].ExitCode != 2 {
		t.Fatalf("paths=%+v", doc.Paths)
	}
	// 1.5 s grid inside 1 s samples snaps back to 1 s: a 500 ms shift.
	if doc.AlignmentErrorMs != 500 {
		t.Fatalf("alignment=%v", doc.AlignmentErrorMs)
	}
	tr := doc.Paths[0].Tracks[0]
	if tr.SilenceMs != 500 || tr.MissingMs != 500 || tr.QualityParts != 48000 {
		t.Fatalf("track=%+v", tr)
	}
	if tr.PeakDBFS == nil || tr.RMSDBFS == nil {
		t.Fatal("audio quality not carried")
	}
}

func TestHashDeterministicAndStored(t *testing.T) {
	in := Input{Seed: 17, SegmentMs: 2000, TargetBps: 640000,
		Runs: []RunInput{testRun("normal", 0)}}
	d1, d2 := Build(in), Build(in)
	h1, err := d1.Hash()
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := d2.Hash()
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash mismatch %s %s", h1, h2)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	stored, err := Build(in).Write(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored != h1 {
		t.Fatalf("stored hash %s != %s", stored, h1)
	}
	var got Document
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("manifest not JSON: %v", err)
	}
	if got.ManifestSHA256 != stored || got.Schema != Schema {
		t.Fatalf("written manifest: hash=%s schema=%s", got.ManifestSHA256, got.Schema)
	}
}

func TestBuildEmpty(t *testing.T) {
	doc := Build(Input{TargetBps: 640000})
	if doc.Segments != 0 || doc.RecoveredTracks == nil || doc.IsolatedTracks == nil {
		t.Fatalf("empty manifest must have empty arrays: %+v", doc)
	}
	if _, err := doc.Hash(); err != nil {
		t.Fatal(err)
	}
}

func TestSegmentRecordExplainsSources(t *testing.T) {
	doc := Build(Input{TargetBps: 640000, Runs: []RunInput{testRun("normal", 0)}})
	segs := doc.Paths[0].Segments
	if len(segs) != 2 {
		t.Fatalf("segments=%+v", segs)
	}
	if segs[0].SampleCount != 1 || len(segs[0].Sources) != 1 ||
		segs[0].Sources[0].File != "a.mp4" || segs[0].Sources[0].Seq != 1 {
		t.Fatalf("segment 0 provenance: %+v", segs[0])
	}
	// The tail is clamped to the content end (2 s), not the 1.5 s grid
	// target, because the remaining content is shorter than one window.
	if segs[1].RequestedEndNs != 2_000_000_000 {
		t.Fatalf("requested end lost: %+v", segs[1])
	}
}
