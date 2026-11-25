package media

import "testing"

func TestBuildMP4ArtifactRoundTrip(t *testing.T) {
	desc := SampleDescription{Channels: 2, BitsPer: 16, SampleRate: 48000}
	keys := KeyframeIndex{SampleNumbers: []uint32{1, 480, 960, 1440}}
	art, err := BuildMP4Artifact(desc, keys)
	if err != nil {
		t.Fatalf("BuildMP4Artifact failed: %v", err)
	}
	if art.BoxBytes <= 0 {
		t.Fatal("serialized skeleton must have non-zero size")
	}
	if art.Description.Channels != 2 || art.Description.SampleRate != 48000 {
		t.Fatalf("description not preserved: %+v", art.Description)
	}
	if len(art.Keyframes.SampleNumbers) != 4 || art.Keyframes.SampleNumbers[2] != 960 {
		t.Fatalf("keyframes not preserved: %+v", art.Keyframes.SampleNumbers)
	}
}

func TestBuildMP4ArtifactRejectsInvalid(t *testing.T) {
	if _, err := BuildMP4Artifact(SampleDescription{Channels: 0, BitsPer: 16, SampleRate: 48000}, KeyframeIndex{}); err == nil {
		t.Fatal("zero channels must be rejected")
	}
	if _, err := BuildMP4Artifact(
		SampleDescription{Channels: 2, BitsPer: 16, SampleRate: 48000},
		KeyframeIndex{SampleNumbers: []uint32{1, 0}},
	); err == nil {
		t.Fatal("zero-based stss entry must be rejected")
	}
}

func TestBuildMP4ArtifactDeterministic(t *testing.T) {
	desc := SampleDescription{Channels: 1, BitsPer: 24, SampleRate: 44100}
	keys := KeyframeIndex{SampleNumbers: []uint32{1, 735}}
	a, err := BuildMP4Artifact(desc, keys)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildMP4Artifact(desc, keys)
	if err != nil {
		t.Fatal(err)
	}
	if a.BoxBytes != b.BoxBytes {
		t.Fatalf("serialization size not deterministic: %d vs %d", a.BoxBytes, b.BoxBytes)
	}
}
