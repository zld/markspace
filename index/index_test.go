package index_test

import (
	"bytes"
	"testing"

	"mediacore"
	"mediacore/index"
)

func trackFor(id uint32) mediacore.TrackID {
	if id == 1 {
		return mediacore.TrackVideo1
	}
	return mediacore.TrackAudio1
}

func buildMP4(t *testing.T) []byte {
	t.Helper()
	// Video: 6 samples, VFR deltas (alternating), every 3rd is a keyframe.
	videoData := bytes.Repeat([]byte{0x10}, 60)
	tracks := []index.WriteTrack{
		{
			TrackID: 1, Timescale: 90000, IsVideo: true, Width: 320, Height: 240,
			Deltas:    []uint32{3000, 3000, 6000, 3000, 3000, 6000},
			Sizes:     []uint32{10, 10, 10, 10, 10, 10},
			Keyframes: []bool{true, false, false, true, false, false},
			SampleData: videoData,
		},
		{
			TrackID: 2, Timescale: 8000, IsVideo: false, Volume: 0x0100,
			Deltas:     []uint32{80, 80, 80, 80},
			Sizes:      []uint32{8, 8, 8, 8},
			SampleData: bytes.Repeat([]byte{0x20}, 32),
		},
	}
	raw, err := index.WriteMP4(tracks)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return raw
}

func TestParseSampleTableAndKeyframes(t *testing.T) {
	raw := buildMP4(t)
	c, err := index.ParseMP4(trackFor, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(c.Tracks))
	}
	var video, audio index.TrackInfo
	for _, ti := range c.Tracks {
		switch ti.Track {
		case mediacore.TrackVideo1:
			video = ti
		case mediacore.TrackAudio1:
			audio = ti
		}
	}
	if len(video.Samples) != 6 || video.Timescale != 90000 || !video.HasKeyframes {
		t.Fatalf("video track wrong: %+v", video)
	}
	// VFR: third sample presentation = (3000+3000)/90000 s = 66_666_666 ns.
	if got := video.Samples[2].Presentation; got != 66_666_666 {
		t.Fatalf("VFR presentation = %d", got)
	}
	// Keyframes at sample numbers 1 and 4 (1-based).
	kf := index.BuildKeyframeIndex(video)
	if kf.Count() != 2 || kf.Frames()[0] != 0 || kf.Frames()[1] != 3 {
		t.Fatalf("keyframe index wrong: %v", kf.Frames())
	}
	// Random-access lookup picks the keyframe at or before the request.
	idx, _, ok := kf.NearestAt(video.Samples[4].Presentation)
	if !ok || idx != 3 {
		t.Fatalf("nearest keyframe = %d ok=%v", idx, ok)
	}
	requests := []int64{video.Samples[0].Presentation, video.Samples[3].Presentation}
	if rate := kf.HitRate(requests); rate != 1.0 {
		t.Fatalf("keyframe hit rate = %v", rate)
	}
	if len(audio.Samples) != 4 || audio.HasKeyframes {
		t.Fatalf("audio track wrong: %+v", audio)
	}
	// Offsets must point inside the container and sizes must be respected.
	for _, s := range video.Samples {
		if s.ChunkOffset < 0 || int(s.ChunkOffset)+s.Size > len(raw) {
			t.Fatalf("sample offset out of bounds: %+v", s)
		}
	}
}

func TestRoundtripPreservesSamplesTimesKeyframes(t *testing.T) {
	raw := buildMP4(t)
	reports, rewritten, err := index.Roundtrip(raw, trackFor)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	for _, r := range reports {
		if !r.SamplesMatch || !r.TimesMatch || !r.KeyframesMatch {
			t.Fatalf("roundtrip lost data: %+v", r)
		}
	}
	// Re-parsing the rewritten container independently must still agree.
	c1, _ := index.ParseMP4(trackFor, bytes.NewReader(raw))
	c2, err := index.ParseMP4(trackFor, bytes.NewReader(rewritten))
	if err != nil {
		t.Fatal(err)
	}
	if len(c1.Tracks) != len(c2.Tracks) {
		t.Fatal("track count changed across roundtrip")
	}
}

func TestCorruptAndBoundaryMP4(t *testing.T) {
	raw := buildMP4(t)
	// Truncated containers: a box whose declared size runs past EOF must be
	// classified corrupt rather than silently accepted.
	for _, cut := range []int{8, len(raw) / 2, len(raw) - 4} {
		if _, err := index.ParseMP4(trackFor, bytes.NewReader(raw[:cut])); err == nil {
			t.Fatalf("truncated MP4 (cut=%d) accepted", cut)
		} else if me, ok := mediacore.AsError(err); !ok {
			t.Fatalf("unclassified error: %v", err)
		} else if me.Cat != mediacore.CatCorrupt {
			t.Fatalf("wrong category %q for truncated input", me.Cat)
		}
	}
	// A complete ftyp with no moov is an explicitly empty container.
	empty := []byte{0, 0, 0, 8, 'f', 'r', 'e', 'e'}
	if _, err := index.ParseMP4(trackFor, bytes.NewReader(empty)); err == nil {
		t.Fatal("moov-less MP4 accepted")
	} else if me, ok := mediacore.AsError(err); !ok || me.Cat != mediacore.CatEmpty {
		t.Fatalf("moov-less input category wrong: %v", err)
	}
	// Garbage is rejected rather than panicking.
	if _, err := index.ParseMP4(trackFor, bytes.NewReader(make([]byte, 16))); err == nil {
		t.Fatal("garbage MP4 accepted")
	}
}
