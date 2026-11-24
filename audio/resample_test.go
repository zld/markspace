package audio_test

import (
	"testing"

	"mediacheck/audio"
	"mediacheck/timeline"
)

func TestPlanIdentity(t *testing.T) {
	stats, err := audio.Plan(48000, 48000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SamplesIn != stats.SamplesOut || stats.DurationErrorNs != 0 {
		t.Fatalf("identity resample drifted: %+v", stats)
	}
	if stats.DurationInNs != 1_000_000_000 {
		t.Fatalf("want 1s, got %d ns", stats.DurationInNs)
	}
}

func TestPlan48kTo24k(t *testing.T) {
	// 1000 input frames at 48 kHz -> 500 output frames at 24 kHz, exact.
	stats, err := audio.Plan(48000, 24000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SamplesOut != 500 || stats.DurationErrorNs != 0 {
		t.Fatalf("downsample plan wrong: %+v", stats)
	}
}

func TestPlanNonIntegralReportsError(t *testing.T) {
	// 1 frame @48k expressed at 44.1k: ratio 44100/48000 < 1, rounds to 1.
	stats, err := audio.Plan(48000, 44100, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SamplesOut != 1 {
		t.Fatalf("want 1 rounded sample, got %d", stats.SamplesOut)
	}
	// The reported error must equal the real duration difference.
	inNs, _ := timeline.NsFromUnits(1, 48000)
	outNs, _ := timeline.NsFromUnits(1, 44100)
	if stats.DurationErrorNs != outNs-inNs {
		t.Fatalf("duration error %d != %d", stats.DurationErrorNs, outNs-inNs)
	}
}

func TestPlanAccumulatedDriftBounded(t *testing.T) {
	// Over many frames the per-frame rounding error stays under one output
	// sample period even with a "nasty" ratio (48k -> 44.1k).
	stats, err := audio.Plan(48000, 44100, 4_800_000) // 100 seconds
	if err != nil {
		t.Fatal(err)
	}
	periodNs, ok := timeline.NsFromUnits(1, 44100)
	if !ok {
		t.Fatal("clock overflow")
	}
	abs, ok := timeline.AbsInt64(stats.DurationErrorNs)
	if !ok || abs > periodNs {
		t.Fatalf("drift %d exceeds one output period %d", stats.DurationErrorNs, periodNs)
	}
	if stats.SamplesOut != 4_410_000 {
		t.Fatalf("want 4,410,000 output samples, got %d", stats.SamplesOut)
	}
}

func TestPlanRejectsBadRates(t *testing.T) {
	if _, err := audio.Plan(0, 48000, 10); !timeline.IsCategory(err, timeline.CatConfig) {
		t.Fatalf("want config error, got %v", err)
	}
	if _, err := audio.Plan(48000, -1, 10); !timeline.IsCategory(err, timeline.CatConfig) {
		t.Fatalf("want config error, got %v", err)
	}
}

func TestResampleF32Stereo(t *testing.T) {
	// 480 stereo frames of a constant signal: every output value must match.
	in := make([]float32, 480*2)
	for i := range in {
		in[i] = 0.5
	}
	out, stats, err := audio.ResampleF32(in, 48000, 24000, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 240*2 || stats.SamplesOut != 240 {
		t.Fatalf("output geometry wrong: %d frames", stats.SamplesOut)
	}
	for i, v := range out {
		if v != 0.5 {
			t.Fatalf("interpolation changed constant signal at %d: %f", i, v)
		}
	}
}

func TestResampleF32Ramp(t *testing.T) {
	// A linear ramp interpolates exactly regardless of phase.
	in := make([]float32, 480)
	for i := range in {
		in[i] = float32(i)
	}
	out, _, err := audio.ResampleF32(in, 48000, 24000, 1)
	if err != nil {
		t.Fatal(err)
	}
	for n, v := range out {
		want := float32(n) * 2
		if v != want {
			t.Fatalf("ramp sample %d = %f, want %f", n, v, want)
		}
	}
}

func TestResampleF32BadBuffer(t *testing.T) {
	if _, _, err := audio.ResampleF32(make([]float32, 5), 48000, 24000, 2); !timeline.IsCategory(err, timeline.CatConfig) {
		t.Fatalf("want config error for odd stereo buffer, got %v", err)
	}
}
