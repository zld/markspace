package medpipe

import "fmt"

// Standard RTP clock rates for the audio profiles the pipeline accepts.
const (
	AudioRate8k   uint32 = 8000
	AudioRate16k  uint32 = 16000
	AudioRate24k  uint32 = 24000
	AudioRate44k1 uint32 = 44100
	AudioRate48k  uint32 = 48000
)

// Speaker labels follow the CICP/WAV channel-order convention. An explicit
// channel map lists one speaker per coded channel in stream order; unknown or
// duplicated labels make the map incomplete and fail validation.
type Speaker string

const (
	SpeakerFL      Speaker = "FL"  // front left
	SpeakerFR      Speaker = "FR"  // front right
	SpeakerFC      Speaker = "FC"  // front center
	SpeakerLFE     Speaker = "LFE" // low frequency effects
	SpeakerBL      Speaker = "BL"  // back left
	SpeakerBR      Speaker = "BR"  // back right
	SpeakerSL      Speaker = "SL"  // side left
	SpeakerSR      Speaker = "SR"  // side right
	SpeakerUnknown Speaker = ""    // unmapped channel is never valid
)

// knownSpeakers is the closed set of accepted speaker labels.
var knownSpeakers = map[Speaker]struct{}{
	SpeakerFL: {}, SpeakerFR: {}, SpeakerFC: {}, SpeakerLFE: {},
	SpeakerBL: {}, SpeakerBR: {}, SpeakerSL: {}, SpeakerSR: {},
}

// AudioConfig is the target layout an audio track must match exactly.
type AudioConfig struct {
	Track      string    `json:"track"`
	SampleRate uint32    `json:"sample_rate"`
	ChannelMap []Speaker `json:"channel_map"`
}

// ChannelCount returns the number of coded channels implied by the map.
func (a AudioConfig) ChannelCount() int { return len(a.ChannelMap) }

// ValidateAudio checks an observed audio track against its target sample rate
// and explicit channel map. Every failure is fatal: mismatched rates or an
// incomplete/ambiguous map cannot be silently remapped without a codec stage,
// which this pipeline deliberately does not run.
func ValidateAudio(track string, observedRate uint32, observedChannels int, observedMap []Speaker, target AudioConfig) []*PipelineError {
	b := NewErrorBuilder(track)
	var errs []*PipelineError

	if observedRate != target.SampleRate {
		errs = append(errs, b.Fatal(CodeAudioRate,
			fmt.Sprintf("sample rate %d Hz does not match target %d Hz", observedRate, target.SampleRate),
			"resample the track at the capture stage or change the target rate"))
	}

	if observedChannels != target.ChannelCount() {
		errs = append(errs, b.Fatal(CodeAudioChannels,
			fmt.Sprintf("channel count %d does not match explicit map of %d", observedChannels, target.ChannelCount()),
			"provide a channel map with one speaker label per coded channel"))
		return errs // map checks below would only add noise
	}

	seen := make(map[Speaker]struct{}, len(target.ChannelMap))
	for i, want := range target.ChannelMap {
		if _, ok := knownSpeakers[want]; !ok || want == SpeakerUnknown {
			errs = append(errs, b.Fatal(CodeAudioChannels,
				fmt.Sprintf("channel %d has unknown or empty speaker label %q", i, want),
				"label every channel with one of FL, FR, FC, LFE, BL, BR, SL, SR"))
			continue
		}
		if _, dup := seen[want]; dup {
			errs = append(errs, b.Fatal(CodeAudioChannels,
				fmt.Sprintf("speaker %s is assigned to more than one channel", want),
				"give each channel a distinct speaker position"))
			continue
		}
		seen[want] = struct{}{}
		// The observed map, when present, must agree position by position.
		if len(observedMap) == len(target.ChannelMap) {
			if observedMap[i] != want {
				errs = append(errs, b.Fatal(CodeAudioChannels,
					fmt.Sprintf("channel %d observed as %s but target map says %s", i, observedMap[i], want),
					"correct the channel ordering at the capture stage or update the target map"))
			}
		}
	}
	if len(observedMap) > 0 && len(observedMap) != target.ChannelCount() {
		errs = append(errs, b.Fatal(CodeAudioChannels,
			fmt.Sprintf("observed channel map has %d entries, target has %d", len(observedMap), target.ChannelCount()),
			"make the observed and target maps cover the same channel count"))
	}
	return errs
}

// StereoMap and MonoMap are convenience target layouts.
var (
	StereoMap = []Speaker{SpeakerFL, SpeakerFR}
	MonoMap   = []Speaker{SpeakerFC}
)
