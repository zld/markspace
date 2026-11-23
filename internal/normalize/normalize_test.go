package normalize

import (
	"testing"

	"mediainspect/internal/media"
)

func TestAudioPassthrough(t *testing.T) {
	res, errs := Audio(media.AudioMetadata{Layout: "stereo", Channels: 2, SampleRate: 48000},
		DefaultAudioTarget, 1, 10)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if res.LayoutConverted || res.Resampled {
		t.Fatal("matching input must not be converted")
	}
}

func TestAudioDeterministicDownmix(t *testing.T) {
	res, errs := Audio(media.AudioMetadata{Layout: "5.1", Channels: 6, SampleRate: 44100},
		DefaultAudioTarget, 1, 11)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !res.LayoutConverted || !res.Resampled {
		t.Fatal("5.1/44.1k must convert to stereo/48k")
	}
	if res.Normalized.Layout != "stereo" || res.Normalized.SampleRate != 48000 {
		t.Fatalf("got %+v", res.Normalized)
	}
}

func TestAudioRejectsUnmappable(t *testing.T) {
	cases := []media.AudioMetadata{
		{Layout: "octagonal", Channels: 8, SampleRate: 48000}, // unknown layout
		{Layout: "5.1", Channels: 2, SampleRate: 48000},       // channel mismatch
		{Layout: "stereo", Channels: 2, SampleRate: 12345},    // unsupported rate
		{Layout: "mono", Channels: 1, SampleRate: 48000},      // upmix not allowed
	}
	for i, in := range cases {
		res, errs := Audio(in, DefaultAudioTarget, 1, uint16(i))
		if len(errs) == 0 {
			t.Fatalf("case %d: expected error for %+v", i, in)
		}
		if res.Normalized != in {
			t.Fatalf("case %d: rejected input must be returned unchanged", i)
		}
	}
}

func TestColorMissing(t *testing.T) {
	res, errs := Color(media.ColorMetadata{Present: false}, 1, 20)
	if len(errs) != 0 {
		t.Fatalf("missing metadata is an anomaly, not an error: %v", errs)
	}
	if len(res.Anomalies) == 0 || res.Normalized != defaultColor {
		t.Fatalf("got %+v", res)
	}
}

func TestColorConflict(t *testing.T) {
	in := media.ColorMetadata{Primaries: "bt2020", Transfer: "pq", Matrix: "bt709", Range: "limited", Present: true}
	res, errs := Color(in, 1, 21)
	if len(errs) != 0 {
		t.Fatalf("conflict is recorded as anomaly, not error: %v", errs)
	}
	if res.Normalized.Matrix != "bt2020" {
		t.Fatalf("matrix = %q", res.Normalized.Matrix)
	}
	if res.Source.Matrix != "bt709" {
		t.Fatal("source value must be preserved")
	}
	if len(res.Anomalies) == 0 {
		t.Fatal("conflict must be recorded")
	}
}

func TestColorIllegalValue(t *testing.T) {
	in := media.ColorMetadata{Primaries: "bt709", Transfer: "smpte2084x", Matrix: "bt709", Range: "limited", Present: true}
	res, errs := Color(in, 1, 22)
	if len(errs) != 1 || errs[0].Class != media.ClassColor {
		t.Fatalf("expected one color error, got %v", errs)
	}
	if res.Normalized.Transfer != "smpte2084x" {
		t.Fatal("illegal source value must be preserved, not silently replaced")
	}
}
