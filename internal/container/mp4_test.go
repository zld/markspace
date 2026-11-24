package container

import (
	"testing"

	"github.com/local/medialab/internal/fixture"
	"github.com/local/medialab/internal/timeline"
)

func TestParseMP4HappyPath(t *testing.T) {
	spec := fixture.DefaultMP4Spec(7)
	spec.Rotation = 90
	data, manifest := fixture.GenerateMP4(spec)
	meta, err := ParseMP4(data)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(meta.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(meta.Tracks))
	}
	video := meta.Tracks[0]
	audio := meta.Tracks[1]
	if video.Kind != "video" || audio.Kind != "audio" {
		t.Fatalf("kinds = %s/%s", video.Kind, audio.Kind)
	}
	if video.SampleCount != 90 || audio.SampleCount != 180 {
		t.Fatalf("sample counts = %d/%d", video.SampleCount, audio.SampleCount)
	}
	// 90 frames at 30fps = exactly 3s on the master clock.
	if timeline.Millis(video.DurationTicks) != 3000 {
		t.Fatalf("video duration = %dms, want 3000", timeline.Millis(video.DurationTicks))
	}
	if video.Rotation != 90 {
		t.Fatalf("rotation = %d, want 90", video.Rotation)
	}
	if video.Color == nil || video.Color.Primaries != 1 || video.Color.Source != "mp4" {
		t.Fatalf("mp4 color = %+v", video.Color)
	}
	wantKeys := []uint32{1, 16, 31, 46, 61, 76}
	if len(video.KeyframeSampleNumbers) != len(wantKeys) {
		t.Fatalf("keyframes = %v", video.KeyframeSampleNumbers)
	}
	for i, k := range wantKeys {
		if video.KeyframeSampleNumbers[i] != k {
			t.Fatalf("keyframe %d = %d want %d", i, video.KeyframeSampleNumbers[i], k)
		}
	}

	// Samples reproduce the generator manifest offsets and times.
	if len(meta.Samples) != len(manifest.Samples) {
		t.Fatalf("sample rows = %d, want %d", len(meta.Samples), len(manifest.Samples))
	}
	for i, want := range manifest.Samples {
		got := meta.Samples[i]
		if got.Track != want.Track || got.Number != want.Index ||
			got.Offset != want.Offset || got.Size != want.Size || got.TimeTick != want.TimeTick {
			t.Fatalf("sample %d mismatch:\n got %+v\nwant %+v", i, got, want)
		}
		if got.Track == 1 {
			if got.Keyframe != want.Keyframe {
				t.Fatalf("sample %d keyframe flag = %v want %v", i, got.Keyframe, want.Keyframe)
			}
		}
	}
}

func TestParseMP4Truncation(t *testing.T) {
	clean, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(7))
	damaged := fixture.CorruptMP4(clean, fixture.CorruptTruncation)
	_, err := ParseMP4(damaged)
	if err == nil {
		t.Fatal("truncated sample must not parse cleanly")
	}
}

func TestParseMP4MissingIndex(t *testing.T) {
	clean, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(7))
	damaged := fixture.CorruptMP4(clean, fixture.CorruptMissingIndex)
	meta, err := ParseMP4(damaged)
	if err != nil {
		t.Fatalf("stss removal should still parse: %v", err)
	}
	if meta.FileMeta.HasIndex {
		t.Fatal("HasIndex must be false without stss")
	}
	for _, s := range meta.Samples {
		if s.Track == 1 && s.HasKeyIndex {
			t.Fatal("video samples must report no keyframe index")
		}
	}
}
