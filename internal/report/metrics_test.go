package report

import (
	"encoding/json"
	"testing"

	"mediavault/internal/archive"
)

func TestBuilderAggregatesRows(t *testing.T) {
	b := NewBuilder("none")
	rows := []archive.ManifestRow{
		{StageKey: "k1", Track: "demo/t0(video)", Order: 0, FrameSHA: "f0", SegmentSHA: "s0",
			PixelMean: 10, PixelVariance: 2, AlignmentNs: 100, DTSNs: 0},
		{StageKey: "k2", Track: "demo/t0(video)", Order: 1, FrameSHA: "f1", SegmentSHA: "s0",
			PixelMean: 20, PixelVariance: 4, AlignmentNs: 200, DTSNs: 33_366_666,
			ContentDupe: true, TimelineDistinct: true},
		{StageKey: "k3", Track: "demo/t1(audio)", Order: 0, FrameSHA: "a0",
			PeakDBFS: -6, AlignmentNs: 50},
	}
	b.AddRows(rows)
	b.SetChunks([]archive.Chunk{{Index: 0}, {Index: 1}})
	m := b.Build()

	if m.ManifestEntries != 3 {
		t.Fatalf("manifest entries = %d, want 3", m.ManifestEntries)
	}
	if m.ChunksCommitted != 2 {
		t.Fatalf("chunks = %d, want 2", m.ChunksCommitted)
	}
	if m.MaxAlignmentNs != 200 {
		t.Fatalf("max alignment = %d, want 200", m.MaxAlignmentNs)
	}
	if m.ContentDupes != 1 || m.TimelineDistinct != 1 {
		t.Fatal("content/timeline duplicate counters wrong")
	}
	var video, audio *TrackMetrics
	for i := range m.Tracks {
		switch m.Tracks[i].Track {
		case "demo/t0(video)":
			video = &m.Tracks[i]
		case "demo/t1(audio)":
			audio = &m.Tracks[i]
		}
	}
	if video == nil || audio == nil {
		t.Fatal("both tracks must appear")
	}
	if video.PixelMeanAvg != 15 || video.PixelVarAvg != 3 {
		t.Fatalf("video averages wrong mean=%f var=%f", video.PixelMeanAvg, video.PixelVarAvg)
	}
	if audio.PeakDBFS != -6 {
		t.Fatalf("audio peak = %f, want -6", audio.PeakDBFS)
	}
}

func TestReportIsValidJSON(t *testing.T) {
	b := NewBuilder("truncated-box")
	b.AddRows([]archive.ManifestRow{{StageKey: "k", Track: "t", FrameSHA: "f"}})
	b.AddError(ErrorView{Stage: "ingest", Kind: "truncated-box", Offset: 123, Recoverable: false})
	b.MarkFailed()
	data, err := b.Build().JSON()
	if err != nil {
		t.Fatal(err)
	}
	var round map[string]any
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if round["status"] != "failed" || round["fault"] != "truncated-box" {
		t.Fatalf("unexpected report header: %v", round["status"])
	}
}
