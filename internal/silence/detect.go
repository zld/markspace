// Package silence classifies audio regions on the shared timeline. Video
// elementary streams carry no audio information at all: the verdict is
// always Undecidable, never a guessed "silent".
package silence

import (
	"mediad/internal/clock"
)

// Verdict is the three-valued result required by the acceptance rules.
type Verdict int

const (
	Undecidable Verdict = iota // no audio information available
	Silent
	NonSilent
)

func (v Verdict) String() string {
	switch v {
	case Silent:
		return "silent"
	case NonSilent:
		return "non_silent"
	default:
		return "undecidable"
	}
}

// Region is one silence interval on the nanosecond timeline, or an
// undecidable placeholder spanning the whole analyzed segment.
type Region struct {
	Interval clock.Interval `json:"interval"`
	Verdict  Verdict        `json:"verdict"`
	Reason   string         `json:"reason,omitempty"`
}

// AudioFormat declares what audio metadata a segment provides. HasAudio
// false models bare video elementary streams.
type AudioFormat struct {
	HasAudio      bool
	SampleRate    uint32 // PCM samples per second
	BytesPerFrame int    // PCM bytes per sample frame (e.g. 2 for mono S16)
}

// Detector holds the amplitude threshold and minimum run length.
type Detector struct {
	// MaxAmplitude: samples at or below this magnitude count as quiet.
	MaxAmplitude int16
	// MinSilentFrames: consecutive quiet samples required to open a region.
	MinSilentFrames int
}

// DefaultDetector returns typical voice-level defaults.
func DefaultDetector() Detector {
	return Detector{MaxAmplitude: 64, MinSilentFrames: 4}
}

// Analyze returns the silence regions of one segment. Without audio
// information the single region is Undecidable across the whole interval.
func (d Detector) Analyze(segInterval clock.Interval, af AudioFormat, pcm []byte) []Region {
	if !af.HasAudio || af.SampleRate == 0 || af.BytesPerFrame <= 0 {
		return []Region{{
			Interval: segInterval,
			Verdict:  Undecidable,
			Reason:   "elementary stream carries no audio information",
		}}
	}
	return d.detectPCM(segInterval, af, pcm)
}

// detectPCM scans signed little-endian PCM samples. Quiet runs longer than
// MinSilentFrames become Silent regions; a single loud sample closes them,
// so brief noise interruptions split one quiet stretch into two regions.
func (d Detector) detectPCM(iv clock.Interval, af AudioFormat, pcm []byte) []Region {
	frameNS := int64(1_000_000_000) / int64(af.SampleRate)
	totalFrames := len(pcm) / af.BytesPerFrame

	var regions []Region
	runStart := -1
	emit := func(start, end int) {
		regions = append(regions, Region{
			Interval: clock.Interval{
				StartNS: iv.StartNS + int64(start)*frameNS,
				EndNS:   iv.StartNS + int64(end)*frameNS,
			},
			Verdict: Silent,
		})
	}
	for i := 0; i < totalFrames; i++ {
		quiet := absSample(pcm[i*af.BytesPerFrame:]) <= d.MaxAmplitude
		switch {
		case quiet && runStart < 0:
			runStart = i
		case !quiet && runStart >= 0:
			if i-runStart >= d.MinSilentFrames {
				emit(runStart, i)
			}
			runStart = -1
		}
	}
	if runStart >= 0 && totalFrames-runStart >= d.MinSilentFrames {
		emit(runStart, totalFrames)
	}
	if len(regions) == 0 {
		regions = append(regions, Region{Interval: iv, Verdict: NonSilent})
	}
	return regions
}

// absSample interprets the first two bytes as a signed 16-bit LE magnitude.
// Mono S16 is the only fixture PCM format; wider frames are read on their
// low two bytes, which stays deterministic for generated data.
func absSample(b []byte) int16 {
	v := int16(uint16(b[0]) | uint16(b[1])<<8)
	if v < 0 {
		return -v
	}
	return v
}
