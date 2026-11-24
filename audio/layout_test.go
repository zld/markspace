package audio_test

import (
	"bytes"
	"testing"

	"mediacheck/audio"
	"mediacheck/ebml"
	"mediacheck/internal/fixture"
	"mediacheck/timeline"
)

func audioTrack(t *testing.T, cfg fixture.Config) *ebml.TrackSpec {
	t.Helper()
	stream, err := fixture.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ebml.Scan(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range res.Tracks {
		if tr.IsAudio() {
			return tr
		}
	}
	t.Fatal("no audio track")
	return nil
}

func TestResolveHealthyStereo(t *testing.T) {
	tr := audioTrack(t, fixture.Config{Seed: 1, Frames: 2})
	layout, err := audio.Resolve(tr, audio.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if layout.Source != "map" || layout.Channels != 2 {
		t.Fatalf("unexpected layout %+v", layout)
	}
	labels := layout.Labels()
	if labels[0] != "FL" || labels[1] != "FR" {
		t.Fatalf("wrong labels %v", labels)
	}
}

func TestResolveDuplicateRejected(t *testing.T) {
	tr := audioTrack(t, fixture.Config{Seed: 1, Frames: 2, BadLayout: true})
	_, err := audio.Resolve(tr, audio.Config{})
	if !timeline.IsCategory(err, timeline.CatUnsupported) {
		t.Fatalf("want unsupported for duplicate map, got %v", err)
	}
}

func TestResolveUnsupportedPosition(t *testing.T) {
	tr := audioTrack(t, fixture.Config{Seed: 1, Frames: 2})
	tr.ChannelMap[0].Position = 999
	_, err := audio.Resolve(tr, audio.Config{})
	if !timeline.IsCategory(err, timeline.CatUnsupported) {
		t.Fatalf("want unsupported position, got %v", err)
	}
}

func TestResolveMapCountMismatch(t *testing.T) {
	tr := audioTrack(t, fixture.Config{Seed: 1, Frames: 2})
	tr.Channels = 6 // map still has 2 rows
	_, err := audio.Resolve(tr, audio.Config{})
	if !timeline.IsCategory(err, timeline.CatUnsupported) {
		t.Fatalf("want unsupported mismatch, got %v", err)
	}
}

func TestResolveByName(t *testing.T) {
	tr := audioTrack(t, fixture.Config{Seed: 1, Frames: 2})
	tr.ChannelMap = nil
	layout, err := audio.Resolve(tr, audio.Config{})
	if err != nil || layout.Source != "name" || layout.Channels != 2 {
		t.Fatalf("named resolution failed: %+v %v", layout, err)
	}
	// Unknown layout name must fail.
	tr.LayoutName = "22.2 surround"
	if _, err := audio.Resolve(tr, audio.Config{}); !timeline.IsCategory(err, timeline.CatUnsupported) {
		t.Fatalf("want unknown name error, got %v", err)
	}
}

func TestResolveMissingFallback(t *testing.T) {
	tr := audioTrack(t, fixture.Config{Seed: 1, Frames: 2})
	tr.ChannelMap = nil
	tr.LayoutName = ""

	// No fallback and not strict: still an error.
	if _, err := audio.Resolve(tr, audio.Config{}); !timeline.IsCategory(err, timeline.CatConfig) {
		t.Fatalf("want config error, got %v", err)
	}
	// Explicit fallback applied.
	layout, err := audio.Resolve(tr, audio.Config{FallbackLayout: "stereo"})
	if err != nil || layout.Source != "config" {
		t.Fatalf("fallback failed: %v %+v", err, layout)
	}
	// Strict mode rejects even when a fallback name is present.
	if _, err := audio.Resolve(tr, audio.Config{
		FallbackLayout: "stereo", RequireExplicit: true,
	}); !timeline.IsCategory(err, timeline.CatConfig) {
		t.Fatalf("strict mode must reject, got %v", err)
	}
	// Fallback whose channel count disagrees is a config error.
	tr.Channels = 6
	if _, err := audio.Resolve(tr, audio.Config{FallbackLayout: "stereo"}); !timeline.IsCategory(err, timeline.CatConfig) {
		t.Fatalf("want fallback mismatch error, got %v", err)
	}
}
