package media

import (
	"hash/crc32"
	"testing"

	"github.com/zld/media-regress/fixture"
	"github.com/zld/media-regress/internal/error"
)

func parseFixture(t *testing.T, name string, format fixture.Format) *Asset {
	t.Helper()
	f, err := fixture.Generate(fixture.Options{Name: name, Format: format})
	if err != nil {
		t.Fatalf("generate %s/%s: %v", name, format, err)
	}
	asset, err := Parse(f.Bytes, name+"."+string(format))
	if err != nil {
		t.Fatalf("parse %s/%s: %v", name, format, err)
	}
	return asset
}

func TestParseMP4Valid(t *testing.T) {
	asset := parseFixture(t, fixture.NameValid, fixture.FormatMP4)
	if len(asset.Tracks) != 2 {
		t.Fatalf("got %d tracks want 2", len(asset.Tracks))
	}
	video, audio := asset.Accounting()
	if video != 1 || audio != 1 {
		t.Fatalf("accounting video=%d audio=%d", video, audio)
	}
	v := asset.Tracks[0]
	if v.Kind != KindVideo || v.Video.Rotation != 90 || v.Video.Width != 1920 || v.Video.Height != 1080 {
		t.Fatalf("video track: %+v", v.Video)
	}
	if v.Video.Crop == nil || v.Video.Crop.Width != 1920 || v.Video.Crop.Height != 1080 {
		t.Fatalf("crop priority not respected: %+v", v.Video.Crop)
	}
	if v.Video.Colour == nil || v.Video.Colour.Primaries != 1 {
		t.Fatalf("colour priority not respected: %+v", v.Video.Colour)
	}
	if v.Conflicts != 2 {
		t.Fatalf("video conflicts: got %d want 2", v.Conflicts)
	}
	a := asset.Tracks[1]
	if a.Kind != KindAudio || a.Audio.SampleRate != 48000 || a.Audio.Channels != 2 || a.Audio.Layout != "stereo" {
		t.Fatalf("audio track: %+v", a.Audio)
	}
	if a.Conflicts != 0 {
		t.Fatalf("audio conflicts: got %d", a.Conflicts)
	}
	if len(a.Samples) != fixture.AudioBlocks {
		t.Fatalf("got %d audio samples want %d", len(a.Samples), fixture.AudioBlocks)
	}
	for i, s := range a.Samples {
		want := fixture.FixedCRC(i + 1)
		if !s.HasCRC || s.CRC32 != want {
			t.Fatalf("sample %d crc=%x want=%x", i+1, s.CRC32, want)
		}
		if len(s.Data) != fixture.AudioBlockBytes {
			t.Fatalf("sample %d length %d", i+1, len(s.Data))
		}
	}
	if asset.Digest == "" {
		t.Fatal("digest must be set")
	}
}

func TestParseMXFValid(t *testing.T) {
	asset := parseFixture(t, fixture.NameValid, fixture.FormatMXF)
	video, audio := asset.Accounting()
	if video != 1 || audio != 1 {
		t.Fatalf("accounting video=%d audio=%d", video, audio)
	}
	v := asset.Tracks[0]
	if v.Video.Rotation != 90 || v.Video.Width != 1920 || v.Video.Crop.Width != 1920 {
		t.Fatalf("video: %+v", v.Video)
	}
	if v.Conflicts != 2 {
		t.Fatalf("video conflicts: got %d want 2", v.Conflicts)
	}
	a := asset.Tracks[1]
	if a.Audio.SampleRate != 48000 || len(a.Samples) != fixture.AudioBlocks {
		t.Fatalf("audio: %+v samples=%d", a.Audio, len(a.Samples))
	}
	for i, s := range a.Samples {
		if s.CRC32 != fixture.FixedCRC(i+1) {
			t.Fatalf("sample %d crc mismatch", i+1)
		}
	}
}

func TestParseMP4BoundaryRejectsTruncatedSTTS(t *testing.T) {
	f, err := fixture.Generate(fixture.Options{Name: fixture.NameBoundary, Format: fixture.FormatMP4})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Parse(f.Bytes, "boundary.mp4")
	if err == nil {
		t.Fatal("expected truncated timestamp error")
	}
	e, ok := merr.As(err)
	if !ok {
		t.Fatalf("error not structured: %v", err)
	}
	if e.Stage != merr.StageProbe {
		t.Fatalf("stage: got %s want probe (%v)", e.Stage, err)
	}
	if e.TrackID != fixture.AudioTrackID {
		t.Fatalf("track: got %d want %d", e.TrackID, fixture.AudioTrackID)
	}
	if e.Kind != merr.KindTimestamp {
		t.Fatalf("kind: got %s want %s", e.Kind, merr.KindTimestamp)
	}
}

func TestParseCorruptBlockKeepsEssence(t *testing.T) {
	f, err := fixture.Generate(fixture.Options{Name: fixture.NameCorrupt, Format: fixture.FormatMP4, CorruptBlock: 3})
	if err != nil {
		t.Fatal(err)
	}
	asset, err := Parse(f.Bytes, "corrupt.mp4")
	if err != nil {
		t.Fatalf("parser must not reject essence damage: %v", err)
	}
	a := asset.Tracks[1]
	recorded := a.Samples[2].CRC32
	if recorded != fixture.FixedCRC(3) {
		t.Fatalf("recorded checksum %x should describe the intact block %x", recorded, fixture.FixedCRC(3))
	}
	if got := crc32.ChecksumIEEE(a.Samples[2].Data); got == recorded {
		t.Fatal("fixture did not damage block 3 essence")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte("not a container at all"), "x"); err == nil {
		t.Fatal("expected probe error")
	} else if merr.StageOf(err) != merr.StageProbe {
		t.Fatalf("stage: %v", err)
	}
	if _, err := Parse(nil, "empty"); err == nil {
		t.Fatal("expected probe error for empty input")
	}
}
