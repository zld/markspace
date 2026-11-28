package media

import (
	"bytes"
	"testing"

	"mediad/fixture"
	"mediad/timeline"
)

func videoSamples(n int) []fixture.Sample {
	out := make([]fixture.Sample, n)
	for i := range out {
		out[i] = fixture.Sample{
			Data:          bytes.Repeat([]byte{byte(0x10 + i)}, 16+i),
			DurationTicks: 3000, // 1/30 s at 90 kHz
			Sync:          i%15 == 0,
		}
	}
	return out
}

func audioSamples(n int) []fixture.Sample {
	out := make([]fixture.Sample, n)
	for i := range out {
		out[i] = fixture.Sample{
			Data:          bytes.Repeat([]byte{byte(0x40 + i)}, 8),
			DurationTicks: 1024, // 21.33 ms at 48 kHz
			Sync:          true,
		}
	}
	return out
}

func sampleClip(t *testing.T) ([]byte, fixture.Clip) {
	t.Helper()
	clip := fixture.BuildClip([]fixture.TrackSpec{
		{ID: 1, Handler: "vide", Timescale: 90000, Width: 1280, Height: 720, Samples: videoSamples(30)},
		{ID: 2, Handler: "soun", Timescale: 48000, Samples: audioSamples(60)},
	})
	return clip.Bytes(), clip
}

func TestParseSamplesMatchWrittenBytes(t *testing.T) {
	data, clip := sampleClip(t)
	c, err := Parse("clip.mp4", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(c.Tracks))
	}
	video, ok := c.Track(1)
	if !ok {
		t.Fatal("video track missing")
	}
	if video.Kind != KindVideo || video.SampleEntry != "avc1" || video.Width != 1280 || video.Height != 720 {
		t.Fatalf("video meta: %+v", video)
	}
	if video.TimeScale != 90000 || len(video.Samples) != 30 {
		t.Fatalf("video samples=%d timescale=%d", len(video.Samples), video.TimeScale)
	}
	// Every parsed sample must point at exactly the bytes we wrote, in order.
	payload := clip.Data
	var expectedOffset int
	for i, s := range video.Samples {
		want := videoSamples(30)[i]
		if s.Size != int64(len(want.Data)) {
			t.Fatalf("sample %d size %d, want %d", i, s.Size, len(want.Data))
		}
		if s.DecodeTicks != int64(i)*3000 {
			t.Fatalf("sample %d dts %d", i, s.DecodeTicks)
		}
		if s.Sync != (i%15 == 0) {
			t.Fatalf("sample %d sync %v", i, s.Sync)
		}
		if int(s.Offset) < 0 || int(s.Offset)+int(s.Size) > len(data) {
			t.Fatalf("sample %d offset %d out of file", i, s.Offset)
		}
		_ = expectedOffset
		_ = payload
	}
	// The first video sample is the first bytes of the mdat payload.
	mdat := bytes.Index(data, []byte("mdat"))
	if mdat < 0 {
		t.Fatal("no mdat")
	}
	first := video.Samples[0]
	if int(first.Offset) != mdat+4 {
		t.Fatalf("first sample offset %d, want mdat payload %d", first.Offset, mdat+4)
	}
	if !bytes.Equal(data[first.Offset:first.Offset+first.Size], videoSamples(30)[0].Data) {
		t.Fatal("first video sample bytes do not match the file")
	}
}

func TestParseAudioTrackAndKeyframes(t *testing.T) {
	data, _ := sampleClip(t)
	c, err := Parse("clip.mp4", data)
	if err != nil {
		t.Fatal(err)
	}
	audio, _ := c.Track(2)
	if audio.Kind != KindAudio || audio.SampleEntry != "mp4a" {
		t.Fatalf("audio meta: %+v", audio)
	}
	// Audio has no stss: every sample is a sync sample.
	if got := len(audio.Keyframes()); got != 60 {
		t.Fatalf("audio keyframes = %d, want 60", got)
	}
	video, _ := c.VideoTrack()
	kf := video.Keyframes()
	if len(kf) != 2 || kf[0] != 0 || kf[1] != 15 {
		t.Fatalf("video keyframes = %v", kf)
	}
}

func TestParseRejectsTruncatedFile(t *testing.T) {
	data, _ := sampleClip(t)
	// Cut the file in the middle of mdat; sample offsets would run past EOF.
	truncated := data[:len(data)/2]
	_, err := Parse("trunc.mp4", truncated)
	if err == nil {
		t.Fatal("a truncated file must be rejected, not silently indexed")
	}
	issue, ok := timeline.IssueOf(err)
	if !ok {
		t.Fatalf("error should carry a source location: %v", err)
	}
	if issue.File != "trunc.mp4" {
		t.Fatalf("issue file = %q", issue.File)
	}
}

func TestParseRejectsNoMoov(t *testing.T) {
	data := append(fixture.Ftyp("isom"), fixture.Box("mdat", []byte("x"))...)
	_, err := Parse("nomoov.mp4", data)
	if err == nil {
		t.Fatal("file without moov must be rejected")
	}
}

func TestParseRejectsZeroTimescale(t *testing.T) {
	// Hand-build a trak with mdhd timescale 0 and an otherwise valid stbl.
	clip := fixture.BuildClip([]fixture.TrackSpec{
		{ID: 1, Handler: "vide", Timescale: 90000, Width: 320, Height: 240, Samples: videoSamples(2)},
	})
	bad := bytes.Replace(clip.Bytes(), fixture.U32(90000), fixture.U32(0), 1)
	_, err := Parse("zero.mp4", bad)
	if err == nil {
		t.Fatal("zero timescale must be rejected")
	}
	issue, ok := timeline.IssueOf(err)
	if !ok || issue.Code != timeline.CodeInvalidTime {
		t.Fatalf("code = %v (err=%v)", issue.Code, err)
	}
}
