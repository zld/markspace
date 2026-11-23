// Package normalize plans audio layout/sample-rate normalization and
// arbitrates color metadata by explicit source priority. It produces plans
// and decisions only; no PCM or coded samples are rewritten here.
package normalize

import (
	"fmt"

	"mediadist/timeline"
)

// DefaultTargetRate is the canonical output sample rate for edge delivery.
const DefaultTargetRate uint32 = 48000

// MixGain is a rational linear gain (Num/Den). Rationals keep the downmix
// description exact; no floating point enters the timeline or manifests.
type MixGain struct {
	Num uint32
	Den uint32
}

// ChannelMix maps one output channel to zero or more source channels.
type ChannelMix struct {
	OutputIndex int
	Sources     []SourceGain
}

// SourceGain pairs a source channel index with its rational gain.
type SourceGain struct {
	SourceIndex int
	Gain        MixGain
}

// ChannelMap is the full speaker mapping for one layout conversion.
type ChannelMap struct {
	InLayout  timeline.ChannelLayout
	OutLayout timeline.ChannelLayout
	Mixes     []ChannelMix
}

// Standard downmix gains as exact rationals.
var (
	gainUnity    = MixGain{Num: 1, Den: 1}
	gainCenter   = MixGain{Num: 707, Den: 1000} // ~-3 dB
	gainSurround = MixGain{Num: 500, Den: 1000}
)

// PlanChannel returns the explicit channel mapping for a layout conversion.
// Unsupported layouts are rejected rather than guessed.
func PlanChannel(in timeline.ChannelLayout) (ChannelMap, error) {
	switch in {
	case timeline.LayoutMono:
		// Mono duplicates into L and R.
		return ChannelMap{in, timeline.LayoutStereo, []ChannelMix{
			{0, []SourceGain{{0, gainUnity}}},
			{1, []SourceGain{{0, gainUnity}}},
		}}, nil
	case timeline.LayoutStereo:
		return ChannelMap{in, timeline.LayoutStereo, []ChannelMix{
			{0, []SourceGain{{0, gainUnity}}},
			{1, []SourceGain{{1, gainUnity}}},
		}}, nil
	case timeline.LayoutFiveOne:
		// 5.1 order: 0 L, 1 R, 2 C, 3 LFE, 4 Ls, 5 Rs.
		// L = L + 0.707 C + 0.5 Ls ; R = R + 0.707 C + 0.5 Rs ; LFE dropped.
		return ChannelMap{in, timeline.LayoutStereo, []ChannelMix{
			{0, []SourceGain{{0, gainUnity}, {2, gainCenter}, {4, gainSurround}}},
			{1, []SourceGain{{1, gainUnity}, {2, gainCenter}, {5, gainSurround}}},
		}}, nil
	default:
		return ChannelMap{}, timeline.NewLocError(timeline.CodeBadLayout, timeline.StageNormalize,
			0, -1, "channel-map", fmt.Errorf("layout %v has no mapping", in))
	}
}

// AudioPlan is the deterministic result of audio normalization planning.
type AudioPlan struct {
	TrackID     uint32
	LayoutIn    timeline.ChannelLayout
	LayoutOut   timeline.ChannelLayout
	ChannelsIn  int
	ChannelsOut int
	RateIn      uint32
	RateOut     uint32
	SamplesIn   uint64
	SamplesOut  uint64
	// DurationIn/Out are common-clock tick spans; Delta is their difference.
	DurationIn  uint64
	DurationOut uint64
	DeltaTicks  uint64
	Map         ChannelMap
}

// PlanAudio derives the channel map and integer sample-rate conversion for an
// audio track. Sample counts scale by the exact rational rate ratio; the
// rounding remainder is reported as the duration delta, never accumulated.
func PlanAudio(t *timeline.Track, targetRate uint32) (*AudioPlan, error) {
	if t.Kind != timeline.KindAudio {
		return nil, timeline.NewLocError(timeline.CodeBadArgument, timeline.StageNormalize,
			t.ID, -1, "audio-plan", fmt.Errorf("track %d is not audio", t.ID))
	}
	if targetRate == 0 {
		targetRate = DefaultTargetRate
	}
	cm, err := PlanChannel(t.Layout)
	if err != nil {
		return nil, err
	}
	// Total PCM frames derive from native duration, which is robust to how the
	// demuxer groups frames into sample entries.
	samplesIn := nativePCMFrames(t)
	num := samplesIn * uint64(targetRate)
	samplesOut := num / uint64(t.SampleRate)
	remainder := num % uint64(t.SampleRate)
	durationIn := t.End()
	// Output duration on the common clock uses the same integer scaling the
	// clock itself uses, so the delta is at most one source-tick rounding.
	clock, _ := timeline.NewClock(targetRate)
	durationOut := clock.ToCommon(samplesOut)
	delta := durationIn - durationOut
	if durationOut > durationIn {
		delta = durationOut - durationIn
	}
	return &AudioPlan{
		TrackID: t.ID, LayoutIn: t.Layout, LayoutOut: cm.OutLayout,
		ChannelsIn: t.Layout.Channels(), ChannelsOut: cm.OutLayout.Channels(),
		RateIn: t.SampleRate, RateOut: targetRate,
		SamplesIn: samplesIn, SamplesOut: samplesOut + b2i(remainder > 0),
		DurationIn: durationIn, DurationOut: durationOut, DeltaTicks: delta,
		Map: cm,
	}, nil
}

func b2i(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// nativePCMFrames computes total PCM frames from the track's native duration.
func nativePCMFrames(t *timeline.Track) uint64 {
	if len(t.Samples) == 0 {
		return 0
	}
	clock, err := timeline.NewClock(t.Timescale)
	if err != nil {
		return 0
	}
	nativeEndTicks := clock.FromCommon(t.End())
	return nativeEndTicks * uint64(t.SampleRate) / uint64(t.Timescale)
}
