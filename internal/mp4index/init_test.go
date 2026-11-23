package mp4index

import (
	"bytes"
	"testing"
)

func tracks() []TrackInfo {
	return []TrackInfo{
		{TrackID: 1, Timescale: 90000, DurationMS: 6000, Handler: "vide", Width: 640, Height: 360, Codec: "V_VP9"},
		{TrackID: 2, Timescale: 48000, DurationMS: 6000, Handler: "soun", Width: 0, Height: 0, Codec: "A_OPUS"},
	}
}

func TestBuildInitSegmentDeterministic(t *testing.T) {
	a, err := BuildInitSegment(tracks())
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildInitSegment(tracks())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("init segment is not deterministic")
	}
	if len(a) < 200 {
		t.Fatalf("init segment too small: %d bytes", len(a))
	}
}

func TestBuildInitSegmentParsesWithGoMP4(t *testing.T) {
	data, err := BuildInitSegment(tracks())
	if err != nil {
		t.Fatal(err)
	}
	movieScale, trackScales, err := ParseInitSegment(data)
	if err != nil {
		t.Fatalf("ParseInitSegment: %v", err)
	}
	if movieScale != 1000 {
		t.Fatalf("movie timescale = %d, want 1000", movieScale)
	}
	if trackScales[90000] != 1 || trackScales[48000] != 1 {
		t.Fatalf("track timescales = %+v", trackScales)
	}
}

func TestBuildInitSegmentRejectsEmpty(t *testing.T) {
	if _, err := BuildInitSegment(nil); err == nil {
		t.Fatal("expected error for no tracks")
	}
}

func TestNanoToTimescale(t *testing.T) {
	if got := NanoToTimescale(1_000_000_000, 90000); got != 90000 {
		t.Fatalf("1s at 90kHz = %d", got)
	}
	if got := NanoToTimescale(33_333_333, 90000); got != 2999 {
		t.Fatalf("33.333ms at 90kHz = %d, want 2999", got)
	}
	if got := NanoToTimescale(-1, 90000); got != 0 {
		t.Fatalf("negative = %d", got)
	}
}
