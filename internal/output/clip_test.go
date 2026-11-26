package output

import (
	"testing"

	"replayproof/internal/index"
	"replayproof/internal/parse"
)

func clipFixture(t *testing.T) (*parse.Asset, *parse.Track, string) {
	t.Helper()
	gen := buildClipSource(t)
	asset := gen.asset
	return asset, asset.VideoTrack(), gen.path
}

type clipSource struct {
	asset *parse.Asset
	path  string
}

func TestWriteClipRepackagesSamples(t *testing.T) {
	src, video, path := clipFixture(t)
	entries := make([]index.Entry, 0, 4)
	for i := 1; i < 5; i++ {
		entries = append(entries, index.Entry{
			TrackID: video.ID, Number: video.Samples[i].Number,
			DTS: video.Samples[i].DTS, End: video.Samples[i].End(),
			PTS: video.Samples[i].PTS, Offset: video.Samples[i].Offset,
			Size: video.Samples[i].Size, Keyframe: video.Samples[i].Keyframe,
		})
	}
	spec := ClipSpec{
		EventIndex: 1, Start: entries[0].DTS, End: entries[len(entries)-1].End,
		Video: entries, SourceBytes: int64(len(entries) * 100),
	}
	dir := t.TempDir()
	clipPath := dir + "/clip1.mp4"
	clip, err := WriteClip(path, clipPath, video.Meta, video, nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	if clip.Bytes == 0 || len(clip.Hash) != 64 {
		t.Fatalf("clip=%#v", clip)
	}
	parsed, err := parse.ParseMP4(clipPath)
	if err != nil {
		t.Fatalf("clip does not parse: %v", err)
	}
	vt := parsed.VideoTrack()
	if vt == nil || len(vt.Samples) != 4 {
		t.Fatalf("clip video samples=%v", vt)
	}
	if vt.Samples[0].DTS.Sign() != 0 {
		t.Fatalf("clip must start at zero, got %v", vt.Samples[0].DTS)
	}
	if len(vt.Samples) != len(entries) {
		t.Fatalf("sample count=%d want %d", len(vt.Samples), len(entries))
	}
	for i, e := range entries {
		got := vt.Samples[i]
		if got.Size != e.Size {
			t.Fatalf("sample %d size %d want %d", i, got.Size, e.Size)
		}
		srcData, err := parse.ReadBytes(path, e.Offset, e.Size)
		if err != nil {
			t.Fatal(err)
		}
		dstData, err := parse.ReadBytes(clipPath, got.Offset, got.Size)
		if err != nil {
			t.Fatal(err)
		}
		if string(srcData) != string(dstData) {
			t.Fatalf("sample %d payload changed", i)
		}
		if got.Keyframe != e.Keyframe {
			t.Fatalf("sample %d keyframe %v want %v", i, got.Keyframe, e.Keyframe)
		}
	}
	if vt.Meta == nil || vt.Meta.Rotation == nil || vt.Meta.Rotation.Degrees != 90 {
		t.Fatalf("rotation not preserved: %#v", vt.Meta)
	}
	_ = src
}

func TestWriteClipIsDeterministic(t *testing.T) {
	_, video, path := clipFixture(t)
	entries := []index.Entry{{
		TrackID: video.ID, Number: 1, DTS: video.Samples[0].DTS, End: video.Samples[0].End(),
		PTS: video.Samples[0].PTS, Offset: video.Samples[0].Offset, Size: video.Samples[0].Size, Keyframe: true,
	}}
	spec := ClipSpec{EventIndex: 1, Start: entries[0].DTS, End: entries[0].End, Video: entries}
	dir := t.TempDir()
	a, err := WriteClip(path, dir+"/a.mp4", nil, video, nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := WriteClip(path, dir+"/b.mp4", nil, video, nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatalf("hashes differ: %s vs %s", a.Hash, b.Hash)
	}
}

func TestWriteClipEmpty(t *testing.T) {
	_, video, path := clipFixture(t)
	dir := t.TempDir()
	if _, err := WriteClip(path, dir+"/e.mp4", nil, video, nil, ClipSpec{}); err == nil {
		t.Fatal("expected empty clip error")
	}
}

func TestWriteClipWithAudio(t *testing.T) {
	asset, video, path := clipFixture(t)
	audio := asset.AudioTrack()
	var vEntries, aEntries []index.Entry
	for i := 0; i < 4; i++ {
		s := video.Samples[i]
		vEntries = append(vEntries, index.Entry{TrackID: video.ID, Number: s.Number, DTS: s.DTS, End: s.End(), PTS: s.PTS, Offset: s.Offset, Size: s.Size, Keyframe: s.Keyframe})
	}
	end := vEntries[len(vEntries)-1].End
	for i := 0; i < len(audio.Samples); i++ {
		s := audio.Samples[i]
		if s.DTS.GreaterEq(end) {
			break
		}
		aEntries = append(aEntries, index.Entry{TrackID: audio.ID, Number: s.Number, DTS: s.DTS, End: s.End(), PTS: s.PTS, Offset: s.Offset, Size: s.Size, Keyframe: true})
	}
	spec := ClipSpec{EventIndex: 1, Start: vEntries[0].DTS, End: end, Video: vEntries, Audio: aEntries}
	dir := t.TempDir()
	if _, err := WriteClip(path, dir+"/av.mp4", video.Meta, video, audio, spec); err != nil {
		t.Fatal(err)
	}
	parsed, err := parse.ParseMP4(dir + "/av.mp4")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.AudioTrack() == nil {
		t.Fatal("audio track missing from clip")
	}
	if len(parsed.AudioTrack().Samples) != len(aEntries) {
		t.Fatalf("audio samples=%d want %d", len(parsed.AudioTrack().Samples), len(aEntries))
	}
}

// buildClipSource generates a fixture MP4 and parses it.
func buildClipSource(t *testing.T) clipSource {
	t.Helper()
	path, asset := buildParsedMP4(t)
	return clipSource{asset: asset, path: path}
}
