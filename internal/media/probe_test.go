package media

import (
	"strings"
	"testing"

	"loudscan/internal/ebu"
)

func TestValidateRejectsMissingAudio(t *testing.T) {
	out := ffprobeOutput{}
	if _, err := validate("x", out, nil, Limits{}); err == nil {
		t.Fatal("file without an audio stream accepted")
	} else if !strings.Contains(err.Error(), "no audio stream") {
		t.Errorf("err = %v", err)
	}
}

func stereoStream() ffprobeStream {
	return ffprobeStream{CodecType: "audio", CodecName: "pcm_s16le", SampleRate: "48000", Channels: 2, ChannelLayout: "stereo"}
}

func TestValidateAcceptsStandardLayouts(t *testing.T) {
	out := ffprobeOutput{Streams: []ffprobeStream{stereoStream()}}
	info, err := validate("x", out, nil, Limits{})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if info.Layout.String() != "FL-FR" {
		t.Errorf("layout = %s", info.Layout)
	}
	if info.SampleRate != 48000 || info.Channels != 2 {
		t.Errorf("info = %+v", info)
	}
}

func TestValidateFallsBackToChannelsWhenLayoutMissing(t *testing.T) {
	s := stereoStream()
	s.ChannelLayout = ""
	out := ffprobeOutput{Streams: []ffprobeStream{s}}
	info, err := validate("x", out, nil, Limits{})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if info.Layout.String() != "FL-FR" {
		t.Errorf("layout = %s", info.Layout)
	}
}

func TestValidateRejectsUnknownLayoutWithoutOverride(t *testing.T) {
	s := stereoStream()
	s.Channels = 3
	s.ChannelLayout = "3.0(back)"
	out := ffprobeOutput{Streams: []ffprobeStream{s}}
	_, err := validate("x", out, nil, Limits{})
	if err == nil || !strings.Contains(err.Error(), "--layout") {
		t.Fatalf("err = %v, want a hint to pass --layout", err)
	}
}

func TestValidateOverrideMustMatchChannelCount(t *testing.T) {
	out := ffprobeOutput{Streams: []ffprobeStream{stereoStream()}}
	_, err := validate("x", out, []string{"FL", "FR", "FC", "LFE", "BL", "BR"}, Limits{})
	if err == nil || !strings.Contains(err.Error(), "declares 6 channels") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateOverrideWithDuplicateLabels(t *testing.T) {
	out := ffprobeOutput{Streams: []ffprobeStream{stereoStream()}}
	if _, err := validate("x", out, []string{"FL", "FL"}, Limits{}); err == nil {
		t.Fatal("duplicate labels accepted")
	}
}

func TestValidateUnsupportedSampleRate(t *testing.T) {
	s := stereoStream()
	s.SampleRate = "32000"
	out := ffprobeOutput{Streams: []ffprobeStream{s}}
	if _, err := validate("x", out, nil, Limits{}); err == nil {
		t.Fatal("32 kHz accepted without K-weighting coefficients")
	}
}

func TestValidateLimits(t *testing.T) {
	s := stereoStream()
	s.Duration = "120"
	out := ffprobeOutput{Streams: []ffprobeStream{s}}
	if _, err := validate("x", out, nil, Limits{MaxSeconds: 60}); err == nil {
		t.Fatal("duration over the limit accepted")
	}
	if _, err := validate("x", out, nil, Limits{MaxSeconds: 200}); err != nil {
		t.Fatalf("duration within the limit rejected: %v", err)
	}
	out.Format.Size = "1000000"
	if _, err := validate("x", out, nil, Limits{MaxBytes: 500, MaxSeconds: -1}); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestSplitLayout(t *testing.T) {
	for _, in := range []string{"FL,FR,FC,LFE", "FL-FR-FC-LFE", "FL FR FC LFE", "FL+FR+FC+LFE"} {
		labels, err := SplitLayout(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if _, err := ebu.NewLayout(labels); err != nil {
			t.Errorf("%q: %v", in, err)
		}
	}
	if _, err := SplitLayout(" , "); err == nil {
		t.Error("empty layout accepted")
	}
}

func TestParseFloatInvalid(t *testing.T) {
	if got := parseFloat("garbage"); got != 0 {
		t.Errorf("parseFloat = %v, want 0", got)
	}
}
