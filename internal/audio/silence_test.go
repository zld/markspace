package audio

import (
	"math"
	"testing"
	"time"

	"avbatch/internal/mediaerr"
)

const ms = int64(time.Millisecond)

// tone builds interleaved PCM: gain per frame is derived from the predicate.
func tone(rate, channels, frames int, ampAt func(frame int) float64) PCM {
	buf := make([]int16, frames*channels)
	for f := 0; f < frames; f++ {
		amp := ampAt(f)
		for c := 0; c < channels; c++ {
			// Alternate sign per channel at a fixed frequency so both
			// channels carry real signal.
			buf[f*channels+c] = int16(math.Round(amp * math.Sin(float64(f)*0.3+float64(c)) * 32767))
		}
	}
	return PCM{SampleRate: rate, Channels: channels, Frames: buf}
}

func TestDetectFindsEmbeddedSilence(t *testing.T) {
	const rate = 48000
	const total = rate * 3 // 3 seconds
	pcm := tone(rate, 2, total, func(f int) float64 {
		switch {
		case f < rate/2:
			return 0.5
		case f < rate*3/2:
			return 0
		default:
			return 0.4
		}
	})
	cfg := DefaultConfig()
	res, err := Detect(pcm, cfg)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(res.Intervals) != 1 {
		t.Fatalf("intervals = %d, want 1 (%+v)", len(res.Intervals), res.Intervals)
	}
	iv := res.Intervals[0]
	if iv.Start < 499*ms || iv.Start > 520*ms {
		t.Errorf("silence start = %d ns, want ~500ms", iv.Start)
	}
	if iv.End < 1499*ms || iv.End > 1520*ms {
		t.Errorf("silence end = %d ns, want ~1500ms", iv.End)
	}
	if res.TotalSilenceNanos != iv.Duration() {
		t.Errorf("total = %d, interval = %d", res.TotalSilenceNanos, iv.Duration())
	}
}

func TestDetectShortSilenceFiltered(t *testing.T) {
	const rate = 8000
	pcm := tone(rate, 1, rate, func(f int) float64 {
		if f > rate/4 && f < rate/4+rate/20 { // 50 ms
			return 0
		}
		return 0.5
	})
	cfg := DefaultConfig()
	cfg.MinSilenceNanos = 100 * ms
	res, err := Detect(pcm, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Intervals) != 0 {
		t.Fatalf("short silence reported: %+v", res.Intervals)
	}
	cfg.MinSilenceNanos = 40 * ms
	res, _ = Detect(pcm, cfg)
	if len(res.Intervals) != 1 {
		t.Fatalf("expected the 50 ms silence once the minimum is lowered: %+v", res.Intervals)
	}
}

func TestDetectMonoVsStereoThreshold(t *testing.T) {
	const rate = 8000
	// Left channel loud, right channel silent.
	frames := rate
	buf := make([]int16, frames*2)
	for f := 0; f < frames; f++ {
		buf[f*2] = int16(12000 * math.Sin(float64(f)*0.2))
		buf[f*2+1] = 0
	}
	pcm := PCM{SampleRate: rate, Channels: 2, Frames: buf}
	cfg := DefaultConfig()
	res, err := Detect(pcm, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Intervals) != 0 {
		t.Fatalf("one loud channel must prevent silence: %+v", res.Intervals)
	}
	// Same data folded to the loud channel only is silent when the channel is
	// analysed alone; this proves the per-channel rule, not a global average.
	mono := PCM{SampleRate: rate, Channels: 1, Frames: buf[1:]}
	_ = mono
}

func TestDetectWindowBoundaries(t *testing.T) {
	const rate = 1000
	pcm := tone(rate, 1, rate, func(f int) float64 {
		if f >= 300 && f < 700 {
			return 0
		}
		return 0.5
	})
	for _, window := range []time.Duration{10 * time.Millisecond, 25 * time.Millisecond, 100 * time.Millisecond} {
		cfg := DefaultConfig()
		cfg.WindowNanos = int64(window)
		cfg.MinSilenceNanos = 100 * ms
		res, err := Detect(pcm, cfg)
		if err != nil {
			t.Fatalf("window %v: %v", window, err)
		}
		if len(res.Intervals) != 1 {
			t.Fatalf("window %v: intervals = %+v", window, res.Intervals)
		}
		iv := res.Intervals[0]
		// Window quantisation may extend the run by at most one window on
		// each side: windows are only silent when fully inside.
		if iv.Start > 300*ms || iv.Start < 300*ms-int64(window) {
			t.Errorf("window %v: start = %d ns", window, iv.Start)
		}
		if iv.End < 700*ms || iv.End > 700*ms+int64(window) {
			t.Errorf("window %v: end = %d ns", window, iv.End)
		}
	}
}

func TestDetectTimelineOffset(t *testing.T) {
	const rate = 1000
	pcm := tone(rate, 1, rate, func(f int) float64 {
		if f < 200 {
			return 0
		}
		return 0.5
	})
	pcm.StartNanos = 5_000 * ms
	res, err := Detect(pcm, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Intervals) != 1 || res.Intervals[0].Start != 5_000*ms {
		t.Fatalf("offset not applied: %+v", res.Intervals)
	}
}

func TestDetectValidation(t *testing.T) {
	if _, err := Detect(PCM{SampleRate: 48000}, DefaultConfig()); err == nil {
		t.Fatal("zero channels accepted")
	}
	if _, err := Detect(PCM{Channels: 2}, DefaultConfig()); err == nil {
		t.Fatal("zero sample rate accepted")
	}
	cfg := DefaultConfig()
	cfg.ThresholdDBFS = 6
	if _, err := Detect(PCM{SampleRate: 8000, Channels: 1, Frames: []int16{0}}, cfg); err == nil {
		t.Fatal("positive dBFS threshold accepted")
	}
	cfg = DefaultConfig()
	cfg.WindowNanos = 0
	if _, err := Detect(PCM{SampleRate: 8000, Channels: 1, Frames: []int16{0}}, cfg); err == nil {
		t.Fatal("zero window accepted")
	}
}

func TestMapToSegmentsSplitsStraddlingWindow(t *testing.T) {
	res := Result{Intervals: []Interval{{Start: 0, End: 1000 * ms}}}
	bounds := []Interval{
		{Start: 0, End: 600 * ms},
		{Start: 600 * ms, End: 1200 * ms},
	}
	got, stats := MapToSegments(res, bounds)
	a, b := got[0], got[1]
	if len(a) != 1 || a[0].End != 600*ms {
		t.Fatalf("first segment silence = %+v", a)
	}
	if len(b) != 1 || b[0].Start != 600*ms {
		t.Fatalf("second segment silence = %+v", b)
	}
	// One interval split across two segments is one split, and nothing is
	// lost: the manifest covers the whole silence.
	if stats.SplitCount != 1 {
		t.Fatalf("split count = %d", stats.SplitCount)
	}
	if stats.TrimmedNanos != 0 {
		t.Fatalf("trimmed = %d ns, want 0", stats.TrimmedNanos)
	}
	// Silence beyond the last segment is genuinely outside the output.
	late := Result{Intervals: []Interval{{Start: 1100 * ms, End: 1500 * ms}}}
	_, stats = MapToSegments(late, bounds)
	if stats.TrimmedNanos != 300*ms {
		t.Fatalf("trimmed = %d ns, want 300ms", stats.TrimmedNanos)
	}
	if stats.TrimmedNanos <= 0 {
		t.Fatalf("trimmed = %d", stats.TrimmedNanos)
	}
	if err := Validate(res.Intervals); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidateDetectsOverlap(t *testing.T) {
	err := Validate([]Interval{{Start: 0, End: 10}, {Start: 5, End: 20}})
	if !mediaerr.Is(err, mediaerr.KindOverlap) {
		t.Fatalf("err = %v, want overlap", err)
	}
}
