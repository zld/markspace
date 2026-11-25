package silence

import (
	"testing"

	"mediad/internal/clock"
)

var iv = clock.Interval{StartNS: 0, EndNS: 1_000_000_000}

func s16(v int16) []byte {
	return []byte{byte(v), byte(uint16(v) >> 8)}
}

func TestBareVideoStreamIsUndecidable(t *testing.T) {
	// No audio information: must never be reported as silent.
	regions := DefaultDetector().Analyze(iv, AudioFormat{HasAudio: false}, nil)
	if len(regions) != 1 || regions[0].Verdict != Undecidable {
		t.Fatalf("bare stream verdict = %+v", regions)
	}
}

func TestContinuousLowAmplitudeIsSilence(t *testing.T) {
	af := AudioFormat{HasAudio: true, SampleRate: 8, BytesPerFrame: 2}
	var pcm []byte
	for i := 0; i < 16; i++ {
		pcm = append(pcm, s16(10)...) // well under threshold 64
	}
	regions := Detector{MaxAmplitude: 64, MinSilentFrames: 4}.Analyze(iv, af, pcm)
	if len(regions) != 1 || regions[0].Verdict != Silent {
		t.Fatalf("want one silent region, got %+v", regions)
	}
	// 16 frames at 8 Hz span exactly 2 s; region covers all samples.
	if regions[0].Interval.StartNS != 0 || regions[0].Interval.EndNS != 2_000_000_000 {
		t.Fatalf("silent interval = %+v", regions[0].Interval)
	}
}

func TestBriefNoiseInterruptsSilence(t *testing.T) {
	af := AudioFormat{HasAudio: true, SampleRate: 8, BytesPerFrame: 2}
	var pcm []byte
	appendN := func(n int, v int16) {
		for i := 0; i < n; i++ {
			pcm = append(pcm, s16(v)...)
		}
	}
	appendN(8, 5)    // quiet run A
	appendN(1, 5000) // one loud sample interrupts
	appendN(8, 5)    // quiet run B
	regions := Detector{MaxAmplitude: 64, MinSilentFrames: 4}.Analyze(iv, af, pcm)
	if len(regions) != 2 {
		t.Fatalf("noise must split silence into two regions, got %+v", regions)
	}
	for _, r := range regions {
		if r.Verdict != Silent {
			t.Fatalf("region verdict = %v", r.Verdict)
		}
	}
	// First region ends exactly at the noise sample (frame 8 => 1 s).
	if regions[0].Interval.EndNS != 1_000_000_000 {
		t.Fatalf("first region end = %d", regions[0].Interval.EndNS)
	}
}

func TestAllLoudIsNonSilent(t *testing.T) {
	af := AudioFormat{HasAudio: true, SampleRate: 8, BytesPerFrame: 2}
	var pcm []byte
	for i := 0; i < 8; i++ {
		pcm = append(pcm, s16(900)...)
	}
	regions := Detector{MaxAmplitude: 64, MinSilentFrames: 2}.Analyze(iv, af, pcm)
	if len(regions) != 1 || regions[0].Verdict != NonSilent {
		t.Fatalf("verdict = %+v", regions)
	}
}

func TestQuietRunBelowMinimumIsNotSilence(t *testing.T) {
	af := AudioFormat{HasAudio: true, SampleRate: 8, BytesPerFrame: 2}
	var pcm []byte
	appendN := func(n int, v int16) {
		for i := 0; i < n; i++ {
			pcm = append(pcm, s16(v)...)
		}
	}
	appendN(2, 0)      // too short
	appendN(2, 9000)   // noise
	appendN(2, 0)      // too short again
	regions := Detector{MaxAmplitude: 64, MinSilentFrames: 4}.Analyze(iv, af, pcm)
	if len(regions) != 1 || regions[0].Verdict != NonSilent {
		t.Fatalf("short quiet runs must not count: %+v", regions)
	}
}
