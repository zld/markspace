package demux_test

import (
	"errors"
	"path/filepath"
	"testing"

	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/fixture"
)

func testSpec() fixture.MP4Spec {
	return fixture.MP4Spec{Tracks: []fixture.TrackSpec{
		{ID: 1, Kind: "video", Timescale: 24000, SampleDuration: 1000, SampleCount: 12, PayloadSize: 64, Width: 320, Height: 180},
		{ID: 2, Kind: "audio", Timescale: 48000, SampleDuration: 1024, SampleCount: 12, PayloadSize: 48, Channels: 2, SampleRate: 48000},
	}}
}

func TestDemuxMP4PreservesSamplesAndTiming(t *testing.T) {
	path := filepath.Join(t.TempDir(), "av.mp4")
	built, err := fixture.WriteMP4(path, testSpec())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := demux.DemuxMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(doc.Tracks))
	}
	for _, tr := range doc.Tracks {
		if len(tr.Samples) != 12 {
			t.Fatalf("track %d samples=%d", tr.ID, len(tr.Samples))
		}
		for i, s := range tr.Samples {
			if s.Index != i || s.Hash != demux.HashPayload(s.Payload) {
				t.Fatalf("bad sample/hash %d/%d", tr.ID, i)
			}
			if s.Offset < 0 || s.Size != len(s.Payload) {
				t.Fatalf("bad offset/size: %+v", s)
			}
		}
	}
	video := doc.Track(1)
	if video.Samples[1].DTS != 1000 || video.Samples[1].PTS != 1000 {
		t.Fatalf("video timing: %+v", video.Samples[1])
	}
	audio := doc.Track(2)
	if audio.Samples[3].DTS != 3*1024 {
		t.Fatalf("audio DTS=%d", audio.Samples[3].DTS)
	}
	if len(built.Samples) != 24 {
		t.Fatalf("built samples=%d", len(built.Samples))
	}
}

func TestDemuxMP4IsolatesCorruptAndTruncatedSamples(t *testing.T) {
	dir := t.TempDir()
	corruptPath := filepath.Join(dir, "corrupt.mp4")
	spec := testSpec()
	spec.Corrupt = []fixture.SampleRef{{TrackID: 1, Index: 4}}
	if _, err := fixture.WriteMP4(corruptPath, spec); err != nil {
		t.Fatal(err)
	}
	doc, err := demux.DemuxMP4(corruptPath)
	if err != nil {
		t.Fatal(err)
	}
	video := doc.Track(1)
	if len(video.Samples) != 11 {
		t.Fatalf("valid video samples=%d, want 11", len(video.Samples))
	}
	if len(doc.Issues) != 1 || doc.Issues[0].Kind != fault.KindCorrupt || doc.Issues[0].TrackID != 1 || doc.Issues[0].Offset < 1 {
		t.Fatalf("issues=%+v", doc.Issues)
	}

	truncPath := filepath.Join(dir, "trunc-sample.mp4")
	spec2 := testSpec()
	spec2.Oversize = []fixture.SampleRef{{TrackID: 2, Index: 11}}
	spec2.OversizeBytes = 1 << 20
	if _, err := fixture.WriteMP4(truncPath, spec2); err != nil {
		t.Fatal(err)
	}
	doc2, err := demux.DemuxMP4(truncPath)
	if err != nil {
		t.Fatal(err)
	}
	audio := doc2.Track(2)
	if len(audio.Samples) != 11 {
		t.Fatalf("valid audio samples=%d, want 11", len(audio.Samples))
	}
	var fe *fault.Error
	if len(doc2.Issues) != 1 || !errors.As(doc2.Issues[0], &fe) || fe.Kind != fault.KindTruncated || !fe.Recoverable {
		t.Fatalf("trunc issues=%+v", doc2.Issues)
	}
}

func TestDemuxTruncatedBoxReportsOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box.mp4")
	if err := fixture.WriteTruncatedBox(path); err != nil {
		t.Fatal(err)
	}
	doc, err := demux.DemuxMP4(path)
	if err == nil {
		t.Fatal("expected box bounds error")
	}
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.KindBoxBounds || fe.Offset != 28 || fe.Recoverable {
		t.Fatalf("error=%#v ok=%v", err, ok)
	}
	if doc == nil || len(doc.Issues) != 1 {
		t.Fatalf("document issues=%+v", doc)
	}
}
