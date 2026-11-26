package fixtures

import (
	"bytes"
	"testing"

	"replayproof/internal/media"
	"replayproof/internal/parse"
)

func TestGenerateMXFParsesTwoTracks(t *testing.T) {
	gen, err := GenerateMXF(DefaultMXFSpec())
	if err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, "ok.mxf", gen.Bytes)
	asset, err := parse.ParseMXF(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(asset.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(asset.Tracks))
	}
	video := asset.VideoTrack()
	audio := asset.AudioTrack()
	if len(video.Samples) != VideoSamples || len(audio.Samples) != AudioSamples {
		t.Fatalf("samples v=%d a=%d", len(video.Samples), len(audio.Samples))
	}
	for i, s := range video.Samples {
		if s.Offset <= 0 || int64(s.Size) <= 0 {
			t.Fatalf("video sample %d has no range", i+1)
		}
		data, err := parse.ReadBytes(path, s.Offset, s.Size)
		if err != nil {
			t.Fatal(err)
		}
		want := formatTag(1, i+1)
		if !bytes.HasPrefix(data, want) {
			t.Fatalf("video sample %d payload tag=%q want %q", i+1, data[:8], want)
		}
		if s.DTS.Cmp(media.FromTicks(int64(i*VideoSampleDur), VideoTimescale)) != 0 {
			t.Fatalf("video sample %d dts=%v", i+1, s.DTS)
		}
	}
	if video.Meta == nil || video.Meta.Rotation == nil || video.Meta.Rotation.Degrees != 180 {
		t.Fatalf("rotation=%#v", video.Meta)
	}
	if video.Meta.Color == nil || video.Meta.Color.Primaries != 1 || video.Meta.Color.Matrix != 1 {
		t.Fatalf("color=%#v", video.Meta.Color)
	}
	if audio.SampleRate != AudioTimescale {
		t.Fatalf("audio rate=%d", audio.SampleRate)
	}
}

func TestGenerateMXFOutOfRangeKLV(t *testing.T) {
	gen, err := GenerateMXF(DefaultMXFSpec())
	if err != nil {
		t.Fatal(err)
	}
	corrupt := CorruptMXFAt(gen.Bytes, gen.EssenceStart)
	path := writeTemp(t, "bad.mxf", corrupt)
	_, err = parse.ParseMXF(path)
	me, ok := mediaAs(err)
	if !ok {
		t.Fatalf("err=%v", err)
	}
	if me.Code != media.CodeMXFOutOfRange {
		t.Fatalf("code=%s", me.Code)
	}
	if me.Offset != gen.EssenceStart {
		t.Fatalf("offset=%d want %d", me.Offset, gen.EssenceStart)
	}
}

func TestGenerateMXFTruncatedToZero(t *testing.T) {
	gen, err := GenerateMXF(DefaultMXFSpec())
	if err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, "zero.mxf", Truncate(gen.Bytes, 0))
	_, err = parse.ParseMXF(path)
	if err == nil {
		t.Fatal("expected error")
	}
}
