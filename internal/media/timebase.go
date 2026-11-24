package media

import "fmt"

const NanosecondsPerSecond int64 = 1_000_000_000

// SupportedSampleRates is the explicit integer-PCM sample-rate allowlist.
var SupportedSampleRates = map[int]bool{
	8000: true, 16000: true, 22050: true, 32000: true,
	44100: true, 48000: true, 96000: true,
}

// SupportedChannels is the explicit channel-count allowlist.
var SupportedChannels = map[int]bool{1: true, 2: true, 4: true, 6: true, 8: true}

// SupportedBitsPerSample is the explicit integer PCM bit-depth allowlist.
var SupportedBitsPerSample = map[int]bool{8: true, 16: true, 24: true, 32: true}

// Timebase converts between integer sample-frame counts and nanoseconds.
// One frame contains one sample for every channel.
type Timebase struct {
	SampleRate int
}

func NewTimebase(sampleRate int) (Timebase, error) {
	if !SupportedSampleRates[sampleRate] {
		return Timebase{}, fmt.Errorf("unsupported sample rate: %d", sampleRate)
	}
	return Timebase{SampleRate: sampleRate}, nil
}

// FramesToNS returns the floor timestamp of frame zero of the given frame.
func (t Timebase) FramesToNS(frames int64) int64 {
	if frames <= 0 || t.SampleRate <= 0 {
		return 0
	}
	return frames * NanosecondsPerSecond / int64(t.SampleRate)
}

// NSToFrame maps an absolute timestamp to its containing frame using floor.
func (t Timebase) NSToFrame(ns int64) int64 {
	if ns <= 0 || t.SampleRate <= 0 {
		return 0
	}
	return ns * int64(t.SampleRate) / NanosecondsPerSecond
}

// FrameDurationNSFloor is the integer floor of one frame's duration.
func (t Timebase) FrameDurationNSFloor() int64 {
	if t.SampleRate <= 0 {
		return 0
	}
	return NanosecondsPerSecond / int64(t.SampleRate)
}

// FrameDurationRemainderNS is the fractional nanosecond numerator remainder.
// Exact duration is FrameDurationNSFloor + remainder/SampleRate nanoseconds.
func (t Timebase) FrameDurationRemainderNS() int64 {
	if t.SampleRate <= 0 {
		return 0
	}
	return NanosecondsPerSecond % int64(t.SampleRate)
}

// AlignmentBoundNS returns the maximum rounding error for one frame.
func (t Timebase) AlignmentBoundNS() int64 {
	d := NanosecondsPerSecond / int64(t.SampleRate)
	if NanosecondsPerSecond%int64(t.SampleRate) != 0 {
		d++
	}
	return d
}
