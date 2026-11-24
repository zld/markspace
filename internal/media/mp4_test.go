package media

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"

	"mediatool/internal/index"
)

func TestInitSegmentIsDecodable(t *testing.T) {
	cfg := TrackConfig{TrackID: 1, Kind: "video", ClockRate: 90000, Width: 1920, Height: 1080,
		Color: &mp4.ColrBox{ColorType: "nclx", ColorPrimaries: 1, TransferCharacteristics: 1, MatrixCoefficients: 1}}
	data, err := InitSegment(cfg)
	if err != nil {
		t.Fatalf("InitSegment: %v", err)
	}
	f, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode init: %v", err)
	}
	if f.Moov == nil || len(f.Moov.Traks) != 1 {
		t.Fatalf("init has no single track")
	}
	if f.Moov.Traks[0].Mdia.Mdhd.Timescale != 90000 {
		t.Fatalf("timescale = %d, want 90000", f.Moov.Traks[0].Mdia.Mdhd.Timescale)
	}
}

func TestMediaSegmentIsDecodableAndOrdered(t *testing.T) {
	samples := []Sample{
		{DurationTicks: 3000, DecodeTimeTicks: 0, Data: []byte("first"), Sync: true},
		{DurationTicks: 3000, DecodeTimeTicks: 3000, Data: []byte("second")},
	}
	data, err := MediaSegment(1, 1, samples, 90000)
	if err != nil {
		t.Fatalf("MediaSegment: %v", err)
	}
	f, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}
	if len(f.Segments) != 1 || len(f.Segments[0].Fragments) != 1 {
		t.Fatalf("expected one fragment, got %d segments", len(f.Segments))
	}
	trun := f.Segments[0].Fragments[0].Moof.Traf.Trun
	if int(trun.SampleCount()) != len(samples) {
		t.Fatalf("sample count = %d, want %d", trun.SampleCount(), len(samples))
	}
}

func TestMediaSegmentIsDeterministic(t *testing.T) {
	samples := []Sample{{DurationTicks: 100, DecodeTimeTicks: 0, Data: []byte("x"), Sync: true}}
	a, err := MediaSegment(1, 1, samples, 48000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := MediaSegment(1, 1, samples, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("segment encoding is not deterministic")
	}
}

func TestColorBoxFromIndex(t *testing.T) {
	c := ColorBox(&index.VideoInfo{ColorKnown: true, Color: "p1/t1/m1/full=false"})
	if c == nil {
		t.Fatal("expected a colr box")
	}
	if c.ColorPrimaries != 1 || c.TransferCharacteristics != 1 || c.MatrixCoefficients != 1 {
		t.Fatalf("color box wrong: %+v", c)
	}
	if ColorBox(&index.VideoInfo{ColorKnown: false}) != nil {
		t.Fatal("unknown color must omit the colr box")
	}
}
