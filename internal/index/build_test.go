package index

import (
	"testing"

	"replayproof/internal/media"
	"replayproof/internal/parse"
)

func testAsset() (*parse.Asset, map[uint32][]parse.Sample) {
	video := &parse.Track{ID: 1, Kind: parse.KindVideo, Timescale: 90000}
	audio := &parse.Track{ID: 2, Kind: parse.KindAudio, Timescale: 48000}
	for i := 0; i < 16; i++ {
		d := media.FromTicks(int64(i*3000), 90000)
		video.Samples = append(video.Samples, parse.Sample{
			Number: i + 1, DTS: d, PTS: d, Duration: media.FromTicks(3000, 90000),
			Offset: int64(1000 + i*100), Size: 100, Keyframe: i%8 == 0,
		})
	}
	for i := 0; i < 16; i++ {
		d := media.FromTicks(int64(i*1024), 48000)
		audio.Samples = append(audio.Samples, parse.Sample{
			Number: i + 1, DTS: d, PTS: d, Duration: media.FromTicks(1024, 48000),
			Offset: int64(5000 + i*50), Size: 50, Keyframe: true,
		})
	}
	return &parse.Asset{Tracks: []*parse.Track{video, audio}}, nil
}

func TestBuildIndexesEverySample(t *testing.T) {
	asset, _ := testAsset()
	idx, err := Build(asset, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(idx.Tracks))
	}
	if got := len(idx.Track(1).Entries); got != 16 {
		t.Fatalf("video entries=%d", got)
	}
	e := idx.Track(1).Entries[3]
	if e.Offset != 1300 || e.Size != 100 || e.Keyframe || e.TrackID != 1 {
		t.Fatalf("entry=%#v", e)
	}
	if e.End.Cmp(e.DTS.Add(media.FromTicks(3000, 90000))) != 0 {
		t.Fatalf("end=%v", e.End)
	}
}

func TestSeekReturnsLatestKeyframeAtOrBefore(t *testing.T) {
	asset, _ := testAsset()
	idx, _ := Build(asset, nil)
	res, err := idx.SeekVideo(1, media.FromTicks(10*3000+1500, 90000))
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.Number != 9 {
		t.Fatalf("entry=%d", res.Entry.Number)
	}
	if !res.Entry.Keyframe {
		t.Fatal("seek must return a keyframe")
	}
	if res.Exact {
		t.Fatal("target lies between samples")
	}
	// The target is 1.5 samples past the keyframe, so the deviation is the
	// full distance back to it.
	want := media.FromTicks(10*3000+1500, 90000).Sub(media.FromTicks(8*3000, 90000))
	if res.Deviation.Cmp(want) != 0 {
		t.Fatalf("deviation=%v want %v", res.Deviation, want)
	}
}

func TestSeekExactBoundary(t *testing.T) {
	asset, _ := testAsset()
	idx, _ := Build(asset, nil)
	res, err := idx.SeekVideo(1, media.FromTicks(8*3000, 90000))
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.Number != 9 || !res.Exact {
		t.Fatalf("res=%#v", res)
	}
}

func TestSeekBeforeFirstKeyframe(t *testing.T) {
	asset, _ := testAsset()
	idx, _ := Build(asset, nil)
	res, err := idx.SeekVideo(1, media.FromTicks(-10, 90000))
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.Number != 1 || res.Exact {
		t.Fatalf("res=%#v", res)
	}
}

func TestSeekUnknownTrackAndKind(t *testing.T) {
	asset, _ := testAsset()
	idx, _ := Build(asset, nil)
	if _, err := idx.SeekVideo(99, media.Zero); err == nil {
		t.Fatal("expected unknown track error")
	}
	if _, err := idx.SeekVideo(2, media.Zero); err == nil {
		t.Fatal("audio is not a video seek target")
	}
	if _, err := idx.Track(2).Seek(media.FromMillis(1)); err != nil {
		t.Fatalf("audio seek: %v", err)
	}
}

func TestBuildUsesRepairedSamples(t *testing.T) {
	asset, _ := testAsset()
	repaired := map[uint32][]parse.Sample{
		1: {{Number: 1, DTS: media.FromMillis(100), Duration: media.FromMillis(10), Offset: 1, Size: 1, Keyframe: true}},
	}
	idx, err := Build(asset, repaired)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Track(1).Entries) != 1 {
		t.Fatalf("entries=%d", len(idx.Track(1).Entries))
	}
	if idx.Track(1).Entries[0].DTS.Cmp(media.FromMillis(100)) != 0 {
		t.Fatalf("dts=%v", idx.Track(1).Entries[0].DTS)
	}
}

func TestBuildNilAsset(t *testing.T) {
	if _, err := Build(nil, nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestRangeAndMergeRanges(t *testing.T) {
	asset, _ := testAsset()
	idx, _ := Build(asset, nil)
	entries := idx.Range(1, media.FromTicks(3000, 90000), media.FromTicks(9000, 90000))
	if len(entries) != 2 || entries[0].Number != 2 || entries[1].Number != 3 {
		t.Fatalf("range=%#v", entries)
	}
	merged := MergeRanges([]ByteRange{{Offset: 100, Size: 50}, {Offset: 150, Size: 10}, {Offset: 400, Size: 5}, {Offset: 120, Size: 10}})
	if len(merged) != 2 {
		t.Fatalf("merged=%#v", merged)
	}
	if merged[0].Offset != 100 || merged[0].Size != 60 {
		t.Fatalf("merged[0]=%#v", merged[0])
	}
	if TotalBytes(merged) != 65 {
		t.Fatalf("total=%d", TotalBytes(merged))
	}
	if MergeRanges(nil) != nil {
		t.Fatal("nil in nil out")
	}
}
