package mux

import (
	"path/filepath"
	"testing"

	"medpipe/internal/demux"
	"medpipe/internal/samplegen"
)

func TestWriteMP4GeneratedTS(t *testing.T) {
	spec := samplegen.TSSpec{IncludePCR: true}
	for i := 0; i < 5; i++ {
		spec.Video = append(spec.Video, samplegen.SampleSpec{PTS: uint64(i) * 3600, Data: []byte{0, 0, 1, byte(i), 0xde, 0xad, 0xbe, 0xef}})
		spec.Audio = append(spec.Audio, samplegen.SampleSpec{PTS: uint64(i) * 1800, Data: []byte{0xff, 0xf1, byte(i), 1, 2, 3}})
	}
	media, events, err := demux.DemuxTS(samplegen.BuildTS(spec))
	if err != nil {
		t.Fatalf("demux: %v events=%+v", err, events)
	}
	maps, merr := demux.SelectTracks(media, nil)
	if merr != nil {
		t.Fatal(merr)
	}
	out := filepath.Join(t.TempDir(), "out.mp4")
	res, werr := WriteMP4(out, maps)
	if werr != nil {
		t.Fatalf("WriteMP4: %v", werr)
	}
	if res.SampleCount != 10 {
		t.Fatalf("samples=%d want 10", res.SampleCount)
	}
	if verr := VerifyReadable(out); verr != nil {
		t.Fatal(verr)
	}
	read, rerr := demux.ReadMP4File(out)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(read.Tracks) != 2 {
		t.Fatalf("mp4 tracks=%d", len(read.Tracks))
	}
	for _, tr := range read.Tracks {
		if len(tr.Samples) != 5 {
			t.Fatalf("codec=%s samples=%d want 5", tr.Codec, len(tr.Samples))
		}
		if len(tr.Samples[0].Data) == 0 {
			t.Fatal("sample data was not read back")
		}
	}
	if string(read.Tracks[0].Samples[0].Data) != string(maps[0].Track.Samples[0].Data) {
		t.Fatal("compressed sample was not preserved losslessly")
	}
}
