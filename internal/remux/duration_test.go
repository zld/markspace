package remux

import (
	"bytes"
	"testing"

	"mediapipeline/internal/demux"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/metadata"
	"mediapipeline/internal/track"
)

func TestNonZeroStartIsPreserved(t *testing.T) {
	// Shift every track's timeline by one second and confirm the remux keeps
	// that start instead of silently moving samples to zero.
	p := fixture.BuildValid(fixture.Options{})
	media, err := demux.ParseMP4(p.Data, demux.Options{SampleEvents: true})
	if err != nil {
		t.Fatal(err)
	}
	for ti := range media.Tracks {
		tr := &media.Tracks[ti]
		for si := range tr.Samples {
			tr.Samples[si].DTS.Ticks += int64(tr.Timescale)
			tr.Samples[si].CTS.Ticks += int64(tr.Timescale)
		}
	}
	for ei := range media.Events {
		media.Events[ei].Time.Ticks += int64(media.Events[ei].Time.Timescale)
	}
	merged, err := metadata.Merge(media.Events)
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]track.Input, len(media.Tracks))
	for i, tr := range media.Tracks {
		inputs[i] = track.Input{ID: tr.ID, Kind: tr.Kind, Timescale: tr.Timescale, Codec: tr.Codec}
	}
	mapping, err := track.Identity(inputs)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	res, err := Write(&out, Plan{
		Src: p.Data, Tracks: media.Tracks, Mapping: mapping,
		Events: merged, MajorBrand: media.MajorBrand,
	})
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := demux.ParseMP4(out.Bytes(), demux.Options{SampleEvents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reparsed.Tracks) != len(media.Tracks) {
		t.Fatalf("tracks: %d", len(reparsed.Tracks))
	}
	for i, tr := range reparsed.Tracks {
		want := media.Tracks[i].Samples[0].DTS.Ticks
		got := tr.Samples[0].DTS.Ticks
		if want != got {
			t.Fatalf("track %d start moved: want %d got %d", tr.ID, want, got)
		}
	}
	gotMerged, err := metadata.Merge(reparsed.Events)
	if err != nil {
		t.Fatal(err)
	}
	if gotMerged.Fingerprint() != merged.Fingerprint() {
		t.Fatalf("event times moved: %s vs %s", gotMerged.Fingerprint(), merged.Fingerprint())
	}
	if res.PayloadSHA256 == "" {
		t.Fatal("missing payload digest")
	}
}
