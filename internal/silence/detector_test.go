package silence

import (
	"math"
	"testing"

	"pwflow/internal/timebase"
)

func pcmWithQuiet(sampleRate, totalMS int, quiet ...timebase.Interval) []byte {
	n := sampleRate * totalMS / 1000
	b := make([]byte, n*2)
	for i := 0; i < n; i++ {
		ns := int64(i) * timebase.Seconds(1) / int64(sampleRate)
		v := int16(8000)
		for _, q := range quiet {
			if q.Contains(ns) {
				v = 0
			}
		}
		b[2*i] = byte(v)
		b[2*i+1] = byte(uint16(v) >> 8)
	}
	return b
}

func TestDetectsOnlySustainedSilence(t *testing.T) {
	pcm := pcmWithQuiet(48000, 1000, timebase.Interval{Lo: 300_000_000, Hi: 700_000_000})
	rep := DetectS16LE(pcm, Options{SampleRate: 48000})
	if len(rep.Silences) != 1 {
		t.Fatalf("silences=%+v windows=%d", rep.Silences, len(rep.Windows))
	}
	got := rep.Silences[0]
	if got.Duration() < timebase.Milliseconds(300) {
		t.Fatalf("silence too short: %+v", got)
	}
	if got.Lo > timebase.Milliseconds(320) || got.Hi < timebase.Milliseconds(680) {
		t.Fatalf("silence bounds inaccurate: %+v", got)
	}
	if rep.PeakDBFS == nil || math.Abs(*rep.PeakDBFS+12.25) > 0.05 {
		t.Fatalf("peak dbfs=%v, want about -12.25", *rep.PeakDBFS)
	}
}

func TestShortQuietRunIsNotSilence(t *testing.T) {
	pcm := pcmWithQuiet(48000, 1000, timebase.Interval{Lo: 400_000_000, Hi: 640_000_000})
	rep := DetectS16LE(pcm, Options{SampleRate: 48000})
	if len(rep.Silences) != 0 {
		t.Fatalf("short quiet range classified as silence: %+v", rep.Silences)
	}
}

func TestPacketGapCannotCountAsSilence(t *testing.T) {
	gap := timebase.Interval{Lo: 300_000_000, Hi: 700_000_000}
	pcm := pcmWithQuiet(48000, 1000, gap)
	rep := DetectS16LE(pcm, Options{SampleRate: 48000, Exclusions: []timebase.Interval{gap}})
	if len(rep.Silences) != 0 {
		t.Fatalf("excluded packet gap produced silence: %+v", rep.Silences)
	}
	if rep.ExcludedCount == 0 {
		t.Fatal("expected at least one excluded RMS window")
	}
}
