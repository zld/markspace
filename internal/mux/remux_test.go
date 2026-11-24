package mux

import (
	"path/filepath"
	"testing"

	"medpipe/internal/demux"
	"medpipe/internal/samplegen"
)

func TestExplicitMappingRemuxesOnlySelectedTrack(t *testing.T) {
	spec := samplegen.MediaSpec(17)
	media, _, err := demux.DemuxTS(samplegen.BuildTS(spec))
	if err != nil {
		t.Fatal(err)
	}
	rules := []demux.MapRule{{FromPID: samplegen.AudioPID, ToIndex: 7, Enabled: true}}
	mappings, merr := demux.SelectTracks(media, rules)
	if merr != nil {
		t.Fatal(merr)
	}
	out := filepath.Join(t.TempDir(), "audio-only.mp4")
	res, werr := WriteMP4(out, mappings)
	if werr != nil {
		t.Fatal(werr)
	}
	if res.TrackCount != 1 || res.SampleCount != len(spec.Audio) {
		t.Fatalf("tracks=%d samples=%d", res.TrackCount, res.SampleCount)
	}
	read, rerr := demux.ReadMP4File(out)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(read.Tracks) != 1 || read.Tracks[0].Codec != "mp4a" {
		t.Fatalf("read tracks=%+v", read.Tracks)
	}
	if len(read.Tracks[0].Samples) != len(spec.Audio) {
		t.Fatalf("read samples=%d want %d", len(read.Tracks[0].Samples), len(spec.Audio))
	}
}
