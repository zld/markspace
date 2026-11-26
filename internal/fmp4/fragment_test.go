package fmp4

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"liveregress/internal/fault"
	"liveregress/internal/fixture"
)

func buildAudioFixture(t *testing.T) (initBytes, segBytes []byte) {
	t.Helper()
	initBytes, err := fixture.BuildInit([]fixture.TrackSpec{{
		TrackID: 1, Timescale: 48000, Handler: "soun", Codec: "ipcm",
		Channels: 3, SampleSize: 24, SampleRate: 48000,
	}})
	if err != nil {
		t.Fatal(err)
	}
	segBytes, err = fixture.BuildSegment([]fixture.FragmentSpec{{
		Sequence: 1, BaseDecodeTime: 0, TrackID: 1,
		Samples: []fixture.SampleSpec{
			{Duration: 1024, Size: 9, Keyframe: true, Payload: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}},
			{Duration: 1024, Size: 9, Payload: make([]byte, 9)},
			{Duration: 1024, Size: 9, Payload: make([]byte, 9)},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return initBytes, segBytes
}

func TestParseInitTracks(t *testing.T) {
	initBytes, _ := buildAudioFixture(t)
	init, err := ParseInitBytes(initBytes, "init.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if len(init.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(init.Tracks))
	}
	tr := init.Tracks[0]
	if tr.TrackID != 1 || tr.Timescale != 48000 || tr.Channels != 3 || tr.SampleSize != 24 || tr.SampleRate != 48000 {
		t.Fatalf("track = %+v", tr)
	}
	if tr.Handler != "soun" || tr.Codec != "ipcm" {
		t.Fatalf("handler/codec = %s/%s", tr.Handler, tr.Codec)
	}
}

func TestParseSegmentSamples(t *testing.T) {
	_, segBytes := buildAudioFixture(t)
	seg, err := ParseSegmentBytes(segBytes, "seg1.m4s")
	if err != nil {
		t.Fatal(err)
	}
	if len(seg.Fragments) != 1 {
		t.Fatalf("fragments = %d", len(seg.Fragments))
	}
	f := seg.Fragments[0]
	if f.TrackID != 1 || f.Sequence != 1 || len(f.Samples) != 3 {
		t.Fatalf("fragment = %+v", f)
	}
	if f.Samples[1].DecodeTime != 1024 || f.Samples[2].DecodeTime != 2048 {
		t.Fatalf("decode times = %d,%d", f.Samples[1].DecodeTime, f.Samples[2].DecodeTime)
	}
	if !f.Samples[0].Keyframe || f.Samples[1].Keyframe {
		t.Fatalf("keyframes = %v,%v", f.Samples[0].Keyframe, f.Samples[1].Keyframe)
	}
	if f.Samples[1].Offset != f.Samples[0].Offset+9 {
		t.Fatalf("offsets = %d,%d", f.Samples[0].Offset, f.Samples[1].Offset)
	}
	raw := segBytes
	for i, s := range f.Samples {
		if int64(len(raw)) < s.Offset+int64(s.Size) {
			t.Fatalf("sample %d outside file", i)
		}
	}
	// The first sample's payload must be the fixture bytes, verbatim.
	if got := raw[f.Samples[0].Offset : f.Samples[0].Offset+9]; string(got) != string([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("payload = %v", got)
	}
}

func TestTruncatedSegment(t *testing.T) {
	_, segBytes := buildAudioFixture(t)
	// Cut inside the mdat payload but after the moof.
	cut := segBytes[:len(segBytes)-10]
	seg, err := ParseSegmentBytes(cut, "seg1.m4s")
	if err == nil {
		t.Fatal("expected a fault")
	}
	fe, ok := fault.As(err)
	if !ok || !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("want truncate, got %v", err)
	}
	if fe.Offset <= 0 {
		t.Fatalf("offset = %d", fe.Offset)
	}
	if seg == nil || len(seg.Fragments) != 1 {
		t.Fatal("complete moof metadata should still be returned")
	}
}

func TestNotFragmented(t *testing.T) {
	initBytes, _ := buildAudioFixture(t)
	_, err := ParseSegmentBytes(initBytes, "init.mp4")
	if !errors.Is(err, fault.ErrFormat) {
		t.Fatalf("want format, got %v", err)
	}
}

func TestReaderFromDisk(t *testing.T) {
	initBytes, segBytes := buildAudioFixture(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "init.mp4"), initBytes)
	writeFile(t, filepath.Join(dir, "seg1.m4s"), segBytes)
	initR, err := Open(filepath.Join(dir, "init.mp4"), "init.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer initR.Close()
	init, err := initR.ParseInit()
	if err != nil {
		t.Fatal(err)
	}
	segR, err := Open(filepath.Join(dir, "seg1.m4s"), "seg1.m4s")
	if err != nil {
		t.Fatal(err)
	}
	defer segR.Close()
	seg, err := segR.ParseSegment()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := segR.ReadSample(seg.Fragments[0].Samples[0], 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes) != 9 {
		t.Fatalf("sample bytes = %d", len(bytes))
	}
	if init.Tracks[0].TimeOf(48000).Ticks != 48000 {
		t.Fatal("TimeOf conversion wrong")
	}
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
