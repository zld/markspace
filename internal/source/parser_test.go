package source_test

import (
	"path/filepath"
	"testing"

	"mediaprep/internal/fixture"
	"mediaprep/internal/model"
	"mediaprep/internal/source"
)

func TestMP4SampleTableRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video.mp4")
	payloads := [][]byte{[]byte("alpha-sample-bytes"), make([]byte, 100), []byte("omega")}
	_, err := fixture.WriteMP4(fixture.MP4Spec{
		Path: path, Width: 320, Height: 180, Payloads: payloads,
		SampleFields: map[string]string{"rotation": "90", "color": "bt709"},
		TrackFields:  map[string]string{"rotation": "180"},
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := source.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if file.Format != "mp4" || len(file.Tracks) != 1 {
		t.Fatalf("got format=%s tracks=%d", file.Format, len(file.Tracks))
	}
	tr := file.Tracks[0]
	if tr.Width != 320 || tr.Height != 180 {
		t.Fatalf("frame=%dx%d", tr.Width, tr.Height)
	}
	if len(tr.Samples) != len(payloads) {
		t.Fatalf("samples=%d want %d", len(tr.Samples), len(payloads))
	}
	for i, want := range payloads {
		if string(tr.Samples[i].Payload) != string(want) {
			t.Fatalf("sample %d payload mismatch", i)
		}
		if tr.Samples[i].Offset <= 0 {
			t.Fatalf("sample %d missing source offset", i)
		}
	}
}

func TestMP4BoxOverrunHasTrackAndOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overrun.mp4")
	_, err := fixture.WriteMP4(fixture.MP4Spec{
		Path: path, Payloads: [][]byte{[]byte("x")}, BoxOverrunInTrak: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ParseMP4File(path)
	pe, ok := model.AsError(err)
	if !ok {
		t.Fatalf("want model.Error, got %T %v", err, err)
	}
	if pe.Code != model.CodeLengthOutOfRange {
		t.Fatalf("code=%s want LENGTH_OUT_OF_RANGE", pe.Code)
	}
	if pe.Track == "" || !pe.HasOffset {
		t.Fatalf("error lacks track/offset: %+v", pe)
	}
}

func TestMXFTruncatedKLVReportsOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "truncated.mxf")
	_, err := fixture.WriteMXF(fixture.MXFSpec{
		Path: path, PCM: make([]byte, 4800), TruncatedKLV: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ParseMXFFile(path)
	pe, ok := model.AsError(err)
	if !ok {
		t.Fatalf("want model.Error, got %v", err)
	}
	if pe.Code != model.CodeTruncated || pe.Track != "mxf:1" || !pe.HasOffset {
		t.Fatalf("unexpected error: %+v", pe)
	}
}

func TestUnsupportedMP4Codec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hvc1.mp4")
	_, err := fixture.WriteMP4(fixture.MP4Spec{
		Path: path, UnsupportedCodec: "hvc1", Payloads: [][]byte{[]byte("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ParseMP4File(path)
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeUnsupportedCodec || pe.Track == "" || !pe.HasOffset {
		t.Fatalf("want locatable unsupported MP4 codec, got %v", err)
	}
}

func TestUnsupportedMXFCodec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compressed.mxf")
	_, err := fixture.WriteMXF(fixture.MXFSpec{Path: path, Codec: "aac", PCM: []byte("data")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ParseMXFFile(path)
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeUnsupportedCodec || !pe.HasOffset {
		t.Fatalf("want locatable unsupported codec, got %v", err)
	}
}
