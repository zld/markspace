// Package normalize performs deterministic audio layout/sample-rate
// normalization and color metadata validation. It never silently converts:
// unmappable layouts, unsupported sample rates and color anomalies are
// reported as per-frame errors or recorded anomalies.
package normalize

import (
	"fmt"

	"mediainspect/internal/media"
)

// AudioTarget is the canonical audio format all frames are normalized to.
type AudioTarget struct {
	Layout     string
	Channels   int
	SampleRate int
}

// DefaultAudioTarget is stereo at 48 kHz.
var DefaultAudioTarget = AudioTarget{Layout: "stereo", Channels: 2, SampleRate: 48000}

// layoutChannels maps a known channel layout to its canonical channel count.
var layoutChannels = map[string]int{
	"mono":   1,
	"stereo": 2,
	"4.0":    4,
	"5.1":    6,
	"7.1":    8,
}

// downmixChain is the deterministic order in which layouts can be reduced.
// A layout may only be converted to one appearing later in the chain.
var downmixChain = []string{"7.1", "5.1", "4.0", "stereo", "mono"}

// supportedSampleRates are the rates this build can resample between.
var supportedSampleRates = map[int]bool{
	8000: true, 16000: true, 24000: true, 44100: true, 48000: true,
}

// AudioResult describes the outcome of normalizing one frame's audio metadata.
type AudioResult struct {
	Normalized     media.AudioMetadata
	LayoutConverted  bool
	Resampled        bool
}

func chainIndex(layout string) int {
	for i, l := range downmixChain {
		if l == layout {
			return i
		}
	}
	return -1
}

// Audio normalizes one frame's audio metadata toward target. Any mismatch
// that cannot be mapped deterministically yields a per-frame error and the
// source metadata is returned unchanged — no silent conversion.
func Audio(in media.AudioMetadata, target AudioTarget, streamID uint32, seq uint16) (AudioResult, []*media.FrameError) {
	res := AudioResult{Normalized: in}
	var errs []*media.FrameError
	mkErr := func(class media.ErrorClass, format string, args ...interface{}) {
		errs = append(errs, &media.FrameError{
			Class: class, StreamID: streamID, Seq: seq,
			Detail: fmt.Sprintf(format, args...),
		})
	}

	srcCh, known := layoutChannels[in.Layout]
	if !known {
		mkErr(media.ClassAudioLayout, "unmappable channel layout %q", in.Layout)
		return res, errs
	}
	if in.Channels != srcCh {
		mkErr(media.ClassAudioLayout,
			"layout %q implies %d channels but frame declares %d", in.Layout, srcCh, in.Channels)
		return res, errs
	}
	if !supportedSampleRates[in.SampleRate] {
		mkErr(media.ClassSampleRate, "unsupported sample rate %d", in.SampleRate)
		return res, errs
	}

	if in.Layout != target.Layout {
		from, to := chainIndex(in.Layout), chainIndex(target.Layout)
		if from < 0 || to < 0 || to <= from {
			mkErr(media.ClassAudioLayout,
				"no deterministic mapping from layout %q to %q", in.Layout, target.Layout)
			return res, errs
		}
		res.Normalized.Layout = target.Layout
		res.Normalized.Channels = target.Channels
		res.LayoutConverted = true
	}
	if in.SampleRate != target.SampleRate {
		if !supportedSampleRates[target.SampleRate] {
			mkErr(media.ClassSampleRate, "unsupported target sample rate %d", target.SampleRate)
			return res, errs
		}
		res.Normalized.SampleRate = target.SampleRate
		res.Resampled = true
	}
	return res, errs
}
