package track

import (
	"errors"
	"testing"

	"rtptrace/internal/manifest"
)

func TestAudioMatrixAndRateAreExplicit(t *testing.T) {
	tr := manifest.Track{ID: 2, Kind: manifest.TrackAudio, Audio: &manifest.AudioMetadata{
		Channels: 6, Layout: "5.1", SampleRate: 44100,
	}}
	plan, err := NormalizeAudio(tr, "stereo", 48000)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Matrix) != 2 || len(plan.Matrix[0]) != 6 {
		t.Fatalf("matrix shape = %dx%d", len(plan.Matrix), len(plan.Matrix[0]))
	}
	if plan.ResampleRatio.String() != "160/147" {
		t.Fatalf("ratio = %s", plan.ResampleRatio)
	}
}

func TestUnsupportedLayoutFailsAtTrack(t *testing.T) {
	tr := manifest.Track{ID: 7, Kind: manifest.TrackAudio, Audio: &manifest.AudioMetadata{
		Channels: 1, Layout: "ambisonic-b", SampleRate: 48000,
	}}
	_, err := NormalizeAudio(tr, "stereo", 48000)
	var ne *NormalizationError
	if !errors.As(err, &ne) || ne.TrackID != 7 || ne.Code != "unsupported-layout" {
		t.Fatalf("expected track 7 layout error, got %v", err)
	}
}

func TestUnsupportedRateFailsAtTrack(t *testing.T) {
	tr := manifest.Track{ID: 8, Kind: manifest.TrackAudio, Audio: &manifest.AudioMetadata{
		Channels: 2, Layout: "stereo", SampleRate: 12345,
	}}
	_, err := NormalizeAudio(tr, "stereo", 48000)
	var ne *NormalizationError
	if !errors.As(err, &ne) || ne.Code != "unsupported-sample-rate" {
		t.Fatalf("expected rate error, got %v", err)
	}
}

func TestResampleIsDeterministic(t *testing.T) {
	in := []int16{0, 100, 200, 300}
	a := ResampleI16(in, 48000, 24000)
	b := ResampleI16(in, 48000, 24000)
	if len(a) != 2 || a[0] != 0 || a[1] != 200 {
		t.Fatalf("downsample = %v", a)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("resampling not deterministic")
		}
	}
}

func TestColorMissingAndConflictArePreserved(t *testing.T) {
	missing := NormalizeColor(manifest.Track{ID: 1})
	if missing.Basis == "" || missing.Selected.Primaries != "" {
		t.Fatalf("missing color not preserved: %+v", missing)
	}
	src := &manifest.ColorMetadata{Primaries: "bt709", Conflict: true, Alternatives: []string{"bt2020"}}
	conflict := NormalizeColor(manifest.Track{ID: 2, Color: src})
	if !conflict.Conflict || conflict.Selected.Primaries != "bt709" || len(conflict.Selected.Alternatives) != 1 {
		t.Fatalf("conflict not preserved: %+v", conflict)
	}
}
