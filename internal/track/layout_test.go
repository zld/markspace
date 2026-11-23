package track

import (
	"errors"
	"testing"

	"mediaarchive/internal/timeline"
)

func TestAudioChannelLayouts(t *testing.T) {
	cases := []struct {
		channels uint32
		want     ChannelLayout
	}{
		{1, LayoutMono},
		{2, LayoutStereo},
		{6, LayoutSurround51},
	}
	for _, c := range cases {
		n := Normalize(RawLayout{
			TrackID: 1, Kind: KindAudio, Channels: c.channels, SampleRate: 48000,
		})
		if n.Isolated || n.ChannelLayout != c.want {
			t.Fatalf("channels=%d -> %+v", c.channels, n)
		}
	}
}

func TestUnsupportedChannelCountIsolated(t *testing.T) {
	n := Normalize(RawLayout{
		TrackID: 7, Kind: KindAudio, Channels: 8, SampleRate: 48000,
	})
	if !n.Isolated || n.ChannelLayout != LayoutIsolated {
		t.Fatalf("8-channel track must be isolated, got %+v", n)
	}
	if len(n.Reasons) == 0 {
		t.Fatal("isolation must record a reason")
	}
	if err := IsolationError(n); !errors.Is(err, timeline.ErrIncompatible) {
		t.Fatalf("want ErrIncompatible, got %v", err)
	}
}

func TestSampleRateMapping(t *testing.T) {
	n48 := Normalize(RawLayout{
		TrackID: 1, Kind: KindAudio, Channels: 2, SampleRate: 48000,
	})
	if n48.Isolated || n48.SampleRate != 48000 || n48.SampleRateMap != "passthrough" {
		t.Fatalf("48k normalization wrong: %+v", n48)
	}
	n44 := Normalize(RawLayout{
		TrackID: 2, Kind: KindAudio, Channels: 2, SampleRate: 44100,
	})
	if n44.Isolated || n44.SampleRate != 48000 ||
		n44.SampleRateMap != "44100->48000-rate-tag-only" {
		t.Fatalf("44.1k normalization wrong: %+v", n44)
	}
}

func TestConflictAndMissingSampleRate(t *testing.T) {
	for _, rate := range []uint32{0, 96000} {
		n := Normalize(RawLayout{
			TrackID: 3, Kind: KindAudio, Channels: 2, SampleRate: rate,
		})
		if !n.Isolated {
			t.Fatalf("rate=%d must isolate, got %+v", rate, n)
		}
	}
}

func TestMissingColorDefaultsExplicitly(t *testing.T) {
	n := Normalize(RawLayout{TrackID: 4, Kind: KindVideo})
	if n.Isolated || n.ColorFamily != ColorBT709 {
		t.Fatalf("missing color should default, got %+v", n)
	}
	if n.ColorMapping != "missing->default-BT.709" {
		t.Fatalf("mapping note = %q", n.ColorMapping)
	}
}

func TestExplicitColorFamilies(t *testing.T) {
	bt709 := Normalize(RawLayout{
		TrackID: 5, Kind: KindVideo,
		ColorPrimaries: 1, TransferChar: 1, MatrixCoeffs: 1,
	})
	if bt709.Isolated || bt709.ColorFamily != ColorBT709 {
		t.Fatalf("BT.709 wrong: %+v", bt709)
	}
	bt601 := Normalize(RawLayout{
		TrackID: 6, Kind: KindVideo,
		ColorPrimaries: 5, TransferChar: 6, MatrixCoeffs: 6,
	})
	if bt601.Isolated || bt601.ColorFamily != ColorBT601 {
		t.Fatalf("BT.601 wrong: %+v", bt601)
	}
}

func TestPartialAndConflictingColorIsolated(t *testing.T) {
	partial := Normalize(RawLayout{
		TrackID: 8, Kind: KindVideo, ColorPrimaries: 1,
	})
	if !partial.Isolated {
		t.Fatalf("partial color must isolate: %+v", partial)
	}
	conflict := Normalize(RawLayout{
		TrackID: 9, Kind: KindVideo,
		ColorPrimaries: 1, TransferChar: 6, MatrixCoeffs: 1,
	})
	if !conflict.Isolated {
		t.Fatalf("conflicting color must isolate: %+v", conflict)
	}
}

func TestNormalizeBatchSplit(t *testing.T) {
	raw := []RawLayout{
		{TrackID: 1, Kind: KindAudio, Channels: 2, SampleRate: 48000},
		{TrackID: 2, Kind: KindAudio, Channels: 2, SampleRate: 96000},
		{TrackID: 3, Kind: KindVideo, ColorPrimaries: 1, TransferChar: 1, MatrixCoeffs: 1},
	}
	survivors, isolated := NormalizeBatch(raw)
	if len(survivors) != 2 || len(isolated) != 1 || isolated[0].TrackID != 2 {
		t.Fatalf("split wrong: survivors=%d isolated=%+v", len(survivors), isolated)
	}
	// Input order is preserved for deterministic output.
	if survivors[0].TrackID != 1 || survivors[1].TrackID != 3 {
		t.Fatalf("order not preserved: %+v", survivors)
	}
}
