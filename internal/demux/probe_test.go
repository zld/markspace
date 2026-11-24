package demux_test

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"

	"mediarc/internal/demux"
	"mediarc/internal/fixture"
)

func TestGoMP4ProbeResolvesStandardTrackTables(t *testing.T) {
	raw, err := demux.EncodeContainer(fixture.GenerateArchive(fixture.DefaultOptions()))
	if err != nil {
		t.Fatal(err)
	}
	info, err := mp4.Probe(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uint32]*mp4.Track{}
	for _, tr := range info.Tracks {
		byID[tr.TrackID] = tr
	}
	video := byID[1]
	audio := byID[2]
	if video == nil || audio == nil {
		t.Fatalf("missing AV tracks: %+v", info.Tracks)
	}
	if video.Timescale != 30000 || len(video.Samples) != 30 {
		t.Fatalf("video probe = timescale %d samples %d", video.Timescale, len(video.Samples))
	}
	if audio.Timescale != 48000 || len(audio.Samples) != 48 {
		t.Fatalf("audio probe = timescale %d samples %d", audio.Timescale, len(audio.Samples))
	}
	if audio.Samples[0].Size == 0 || video.Samples[0].Size == 0 {
		t.Fatal("stsz sample sizes were not probed")
	}
}
