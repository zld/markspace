package spec_test

import (
	"testing"

	"mediasync/internal/model"
	"mediasync/internal/spec"
)

func audioMeta(ch uint16, sr uint32) model.TrackMeta {
	return model.TrackMeta{
		TrackID: "a", Codec: model.CodecOpus, Channels: ch, SampleRate: sr,
	}
}

func TestValidateClean(t *testing.T) {
	rep := spec.Validate(audioMeta(2, 48000), model.TargetSpec{Channels: 2, SampleRate: 48000})
	if rep.NeedsConversion() || rep.RejectReason != "" {
		t.Fatalf("clean track reported diffs: %+v", rep)
	}
}

func TestValidateChannelAndRateDiffs(t *testing.T) {
	rep := spec.Validate(audioMeta(6, 44100), model.TargetSpec{Channels: 2, SampleRate: 48000})
	fields := map[string]bool{}
	for _, d := range rep.Diffs {
		fields[d.Field] = true
	}
	if !fields[spec.FieldChannels] || !fields[spec.FieldSampleRate] {
		t.Fatalf("missing diffs: %+v", rep.Diffs)
	}
	if !rep.ConversionReq {
		t.Fatal("conversion should be required")
	}
}

func TestValidateRejectsZeroChannelAudio(t *testing.T) {
	rep := spec.Validate(audioMeta(0, 48000), model.TargetSpec{Channels: 2})
	if rep.RejectReason == "" {
		t.Fatal("zero-channel audio must be rejected with a reason")
	}
}

func TestValidateColorMissingAndMismatch(t *testing.T) {
	// Video track with no color metadata: every required field diffs.
	vid := model.TrackMeta{TrackID: "v", Codec: model.CodecVideo}
	target := model.TargetSpec{Color: &model.ColorMeta{
		ColorPrimaries: 9, TransferCharacteristics: 16, MatrixCoefficients: 9, FullRange: false,
	}}
	rep := spec.Validate(vid, target)
	if len(rep.Diffs) != 4 {
		t.Fatalf("want 4 missing-color diffs, got %+v", rep.Diffs)
	}

	// Partial mismatch: primaries differ only.
	vid.Color = &model.ColorMeta{ColorPrimaries: 1, TransferCharacteristics: 16, MatrixCoefficients: 9}
	rep = spec.Validate(vid, target)
	if len(rep.Diffs) != 1 || rep.Diffs[0].Field != spec.FieldColorPrim {
		t.Fatalf("want single primaries diff, got %+v", rep.Diffs)
	}
}

func TestValidateZeroTargetMeansNoConstraint(t *testing.T) {
	rep := spec.Validate(audioMeta(8, 22050), model.TargetSpec{})
	if rep.NeedsConversion() {
		t.Fatalf("empty target must not create diffs: %+v", rep)
	}
}

func TestValidateAudioConstraintsDoNotApplyToVideo(t *testing.T) {
	vid := model.TrackMeta{
		TrackID: "v", Codec: model.CodecVideo,
		Color: &model.ColorMeta{ColorPrimaries: 9, TransferCharacteristics: 16, MatrixCoefficients: 9},
	}
	target := model.TargetSpec{
		Channels: 2, SampleRate: 48000,
		Color: &model.ColorMeta{ColorPrimaries: 1, TransferCharacteristics: 16, MatrixCoefficients: 9},
	}
	rep := spec.Validate(vid, target)
	for _, d := range rep.Diffs {
		if d.Field == spec.FieldChannels || d.Field == spec.FieldSampleRate {
			t.Fatalf("audio-only constraint applied to video: %+v", d)
		}
	}
	if len(rep.Diffs) != 1 || rep.Diffs[0].Field != spec.FieldColorPrim {
		t.Fatalf("video should only report the colour diff: %+v", rep.Diffs)
	}
}
