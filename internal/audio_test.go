package medpipe

import "testing"

func TestAudioValid(t *testing.T) {
	target := AudioConfig{Track: "a", SampleRate: AudioRate48k, ChannelMap: StereoMap}
	if errs := ValidateAudio("a", 48000, 2, StereoMap, target); len(errs) != 0 {
		t.Fatalf("valid stereo config rejected: %v", errs)
	}
}

func TestAudioRateMismatchFails(t *testing.T) {
	target := AudioConfig{Track: "a", SampleRate: AudioRate48k, ChannelMap: MonoMap}
	errs := ValidateAudio("a", 44100, 1, MonoMap, target)
	if len(errs) != 1 || errs[0].Code != CodeAudioRate || errs[0].Kind != KindFatal {
		t.Fatalf("want fatal rate error, got %+v", errs)
	}
}

func TestAudioIncompleteMapFails(t *testing.T) {
	target := AudioConfig{Track: "a", SampleRate: AudioRate48k, ChannelMap: []Speaker{SpeakerFL, SpeakerUnknown}}
	errs := ValidateAudio("a", 48000, 2, nil, target)
	if len(errs) == 0 {
		t.Fatal("empty speaker label must fail")
	}
	for _, e := range errs {
		if e.Code != CodeAudioChannels || e.Kind != KindFatal {
			t.Fatalf("want fatal channel error, got %+v", e)
		}
	}
}

func TestAudioDuplicateSpeakerFails(t *testing.T) {
	target := AudioConfig{Track: "a", SampleRate: AudioRate48k, ChannelMap: []Speaker{SpeakerFL, SpeakerFL}}
	if errs := ValidateAudio("a", 48000, 2, nil, target); len(errs) == 0 {
		t.Fatal("duplicate speaker mapping must fail")
	}
}

func TestAudioObservedOrderMismatchFails(t *testing.T) {
	target := AudioConfig{Track: "a", SampleRate: AudioRate48k, ChannelMap: []Speaker{SpeakerFL, SpeakerFR}}
	observed := []Speaker{SpeakerFR, SpeakerFL} // swapped
	errs := ValidateAudio("a", 48000, 2, observed, target)
	if len(errs) != 2 || errs[0].Code != CodeAudioChannels {
		t.Fatalf("want one channel-order error per channel, got %+v", errs)
	}
}

func TestAudioChannelCountMismatch(t *testing.T) {
	target := AudioConfig{Track: "a", SampleRate: AudioRate48k, ChannelMap: StereoMap}
	errs := ValidateAudio("a", 48000, 1, MonoMap, target)
	if len(errs) != 1 || errs[0].Code != CodeAudioChannels {
		t.Fatalf("want count error, got %+v", errs)
	}
}
