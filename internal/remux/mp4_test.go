package remux_test

import (
	"path/filepath"
	"testing"

	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/fixture"
	"mediapack/internal/remux"
)

func TestWriteMP4PreservesPayloadsAndParses(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	_, err := fixture.WriteMP4(src, fixture.MP4Spec{Tracks: []fixture.TrackSpec{
		{ID: 1, Kind: "video", Timescale: 24000, SampleDuration: 1000, SampleCount: 10, PayloadSize: 64},
	}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := demux.DemuxMP4(src)
	if err != nil {
		t.Fatal(err)
	}
	in := doc.Track(1)
	tr := remux.Track{ID: 7, Kind: "video", FourCC: "avc1", Timescale: 48000, Width: 16, Height: 9}
	for _, s := range in.Samples {
		tr.Samples = append(tr.Samples, remux.Sample{
			DTS: s.DTS * 2, PTS: s.PTS * 2, Duration: s.Duration * 2,
			Payload: s.Payload, Hash: s.Hash,
		})
	}
	out := filepath.Join(dir, "out.mp4")
	res, err := remux.WriteMP4(out, 48000, []remux.Track{tr})
	if err != nil {
		t.Fatal(err)
	}
	if res.SampleCount != 10 {
		t.Fatalf("samples=%d", res.SampleCount)
	}
	outDoc, err := demux.DemuxMP4(out)
	if err != nil {
		t.Fatal(err)
	}
	got := outDoc.Track(7)
	if got == nil || len(got.Samples) != 10 {
		t.Fatalf("output track=%+v issues=%+v", got, outDoc.Issues)
	}
	for i, s := range got.Samples {
		if s.Hash != in.Samples[i].Hash {
			t.Fatalf("hash mismatch at %d", i)
		}
	}
}

func TestHashMismatchFailsBatch(t *testing.T) {
	_, err := remux.WriteMP4(filepath.Join(t.TempDir(), "x.mp4"), 48000, []remux.Track{{
		ID: 1, Kind: "audio", Timescale: 48000,
		Samples: []remux.Sample{{DTS: 0, Duration: 960, Payload: []byte("abc"), Hash: "wrong"}},
	}})
	if err == nil {
		t.Fatal("expected hash failure")
	}
	if fe, ok := fault.As(err); !ok || fe.Kind != fault.KindHash {
		t.Fatalf("error=%v", err)
	}
}
