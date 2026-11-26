package fixtures

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"replayproof/internal/media"
	"replayproof/internal/parse"
)

func mediaAs(err error) (*media.Error, bool) {
	var me *media.Error
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestGenerateMP4IsParseable(t *testing.T) {
	gen, err := GenerateMP4(TrackSpec{}, TrackSpec{})
	if err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, "ok.mp4", gen.Bytes)
	asset, err := parse.ParseMP4(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(asset.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(asset.Tracks))
	}
	video := asset.VideoTrack()
	audio := asset.AudioTrack()
	if video == nil || audio == nil {
		t.Fatal("missing tracks")
	}
	if len(video.Samples) != VideoSamples || len(audio.Samples) != AudioSamples {
		t.Fatalf("samples v=%d a=%d", len(video.Samples), len(audio.Samples))
	}
	for i, s := range video.Samples {
		want := gen.Samples[i]
		if s.Offset != want.Offset || s.Size != want.Size {
			t.Fatalf("video sample %d offset/size %d/%d want %d/%d", i+1, s.Offset, s.Size, want.Offset, want.Size)
		}
		if s.DTS.Cmp(media.FromTicks(int64(i*VideoSampleDur), VideoTimescale)) != 0 {
			t.Fatalf("video sample %d dts=%d us", i+1, s.DTS.Micros())
		}
		if s.Keyframe != want.Keyframe {
			t.Fatalf("video sample %d key=%v want %v", i+1, s.Keyframe, want.Keyframe)
		}
		data, err := parse.ReadBytes(path, s.Offset, s.Size)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, want.Payload) {
			t.Fatalf("video sample %d payload mismatch", i+1)
		}
	}
	if video.Meta == nil || video.Meta.Rotation == nil || video.Meta.Rotation.Degrees != 90 {
		t.Fatalf("rotation meta: %#v", video.Meta)
	}
	if video.Meta.Color == nil || video.Meta.Color.Primaries != 1 {
		t.Fatalf("color meta: %#v", video.Meta.Color)
	}
	if audio.ChannelCount != 2 || audio.SampleRate != 48000 {
		t.Fatalf("audio entry: ch=%d rate=%d", audio.ChannelCount, audio.SampleRate)
	}
}

func TestGenerateMP4TruncatedReportsOffset(t *testing.T) {
	gen, err := GenerateMP4(TrackSpec{}, TrackSpec{})
	if err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, "trunc.mp4", gen.Truncated)
	_, err = parse.ParseMP4(path)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	me, ok := mediaAs(err)
	if !ok {
		t.Fatalf("not a media error: %v", err)
	}
	if me.Code != media.CodeTruncatedBox {
		t.Fatalf("code=%s err=%v", me.Code, err)
	}
	// The cut lands inside mdat, whose header starts at the ftyp boundary.
	if me.Offset != 28 {
		t.Fatalf("offset=%d err=%v", me.Offset, err)
	}
}

func TestTruncatedAtExactOffset16(t *testing.T) {
	gen, err := GenerateMP4(TrackSpec{}, TrackSpec{})
	if err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, "cut16.mp4", Truncate(gen.Bytes, 16))
	_, err = parse.ParseMP4(path)
	me, ok := mediaAs(err)
	if !ok {
		t.Fatalf("err=%v", err)
	}
	if me.Code != media.CodeTruncatedBox || me.Offset != 0 {
		t.Fatalf("offset=%d code=%s", me.Offset, me.Code)
	}
}
