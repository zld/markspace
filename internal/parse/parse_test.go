package parse_test

import (
	"os"
	"path/filepath"
	"testing"

	"replayproof/internal/fixtures"
	"replayproof/internal/media"
	"replayproof/internal/parse"
)

func writeFixture(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestParseMP4TracksAndOffsets(t *testing.T) {
	gen, err := fixtures.GenerateMP4(fixtures.TrackSpec{}, fixtures.TrackSpec{})
	if err != nil {
		t.Fatal(err)
	}
	path := writeFixture(t, "ok.mp4", gen.Bytes)
	asset, err := parse.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	if asset.Source != "mp4" || asset.Path != path {
		t.Fatalf("asset=%#v", asset)
	}
	video := asset.VideoTrack()
	audio := asset.AudioTrack()
	if video == nil || audio == nil {
		t.Fatal("both tracks must parse")
	}
	if video.Codec != "avc1" || audio.Codec != "mp4a" {
		t.Fatalf("codecs %q %q", video.Codec, audio.Codec)
	}
	if video.Timescale != fixtures.VideoTimescale || audio.Timescale != fixtures.AudioTimescale {
		t.Fatalf("timescales %d %d", video.Timescale, audio.Timescale)
	}
	// Offsets must point at the payload tags written by the generator.
	for _, s := range video.Samples {
		data, err := parse.ReadBytes(path, s.Offset, s.Size)
		if err != nil {
			t.Fatal(err)
		}
		if !parse.HasMagic(data, "RPLY") {
			t.Fatalf("video sample %d offset %d does not point at payload", s.Number, s.Offset)
		}
	}
	if len(video.Samples) != fixtures.VideoSamples || len(audio.Samples) != fixtures.AudioSamples {
		t.Fatalf("sample counts %d %d", len(video.Samples), len(audio.Samples))
	}
	if !video.Samples[0].Keyframe || video.Samples[1].Keyframe {
		t.Fatal("keyframe flags do not follow the generator's GOP")
	}
}

func TestParseMP4TruncationPosition(t *testing.T) {
	gen, err := fixtures.GenerateMP4(fixtures.TrackSpec{}, fixtures.TrackSpec{})
	if err != nil {
		t.Fatal(err)
	}
	path := writeFixture(t, "cut.mp4", gen.Cut16)
	_, err = parse.ParseMP4(path)
	me, ok := media.AsError(err)
	if !ok {
		t.Fatalf("expected media error, got %v", err)
	}
	if me.Code != media.CodeTruncatedBox {
		t.Fatalf("code=%s", me.Code)
	}
	if me.Offset != 16 {
		t.Fatalf("offset=%d want 16", me.Offset)
	}
	if me.File != path {
		t.Fatalf("file=%q", me.File)
	}
}

func TestParseMP4RejectsNonMP4(t *testing.T) {
	path := writeFixture(t, "junk.mp4", []byte("this is not an mp4 file at all"))
	if _, err := parse.ParseMP4(path); err == nil {
		t.Fatal("expected parse failure")
	}
}

func TestParseMXFTracksAndEssenceOffsets(t *testing.T) {
	gen, err := fixtures.GenerateMXF(fixtures.DefaultMXFSpec())
	if err != nil {
		t.Fatal(err)
	}
	path := writeFixture(t, "ok.mxf", gen.Bytes)
	asset, err := parse.ParseMXF(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(asset.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(asset.Tracks))
	}
	video := asset.VideoTrack()
	if len(video.Samples) != fixtures.VideoSamples {
		t.Fatalf("video samples=%d", len(video.Samples))
	}
	for _, s := range video.Samples {
		data, err := parse.ReadBytes(path, s.Offset, s.Size)
		if err != nil {
			t.Fatal(err)
		}
		if !parse.HasMagic(data, "RPLY") {
			t.Fatalf("MXF sample %d offset %d does not point at essence", s.Number, s.Offset)
		}
	}
}

func TestParseMXFOutOfRangePosition(t *testing.T) {
	gen, err := fixtures.GenerateMXF(fixtures.DefaultMXFSpec())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"offset-zero": fixtures.CorruptMXFAtZero(gen.Bytes),
		"essence":     fixtures.CorruptMXFAt(gen.Bytes, gen.EssenceStart),
	} {
		t.Run(name, func(t *testing.T) {
			path := writeFixture(t, "bad.mxf", data)
			_, err := parse.ParseMXF(path)
			me, ok := media.AsError(err)
			if !ok {
				t.Fatalf("expected media error, got %v", err)
			}
			if me.Code != media.CodeMXFOutOfRange {
				t.Fatalf("code=%s", me.Code)
			}
			want := int64(0)
			if name == "essence" {
				want = gen.EssenceStart
			}
			if me.Offset != want {
				t.Fatalf("offset=%d want %d", me.Offset, want)
			}
		})
	}
}

func TestParseMXFRejectsNonMXF(t *testing.T) {
	path := writeFixture(t, "junk.mxf", []byte("definitely not mxf"))
	if _, err := parse.ParseMXF(path); err == nil {
		t.Fatal("expected parse failure")
	}
}
