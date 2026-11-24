package audio

import (
	"testing"

	"mediad/internal/model"
)

func TestResampleUpsampleAndDownsample(t *testing.T) {
	// 2 periods at 1000 Hz -> 2000 Hz gives 4 output periods.
	in := Frame{SampleRate: 1000, Channels: 1, Samples: []int16{0, 1000}}
	out, st, err := Resample(in, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if st.OutputPeriods != 4 || st.OutputRate != 2000 {
		t.Fatalf("stats wrong: %+v", st)
	}
	// Positions: 0, 0.5, 1(clamped), 1(clamped) -> 0, 500, 1000, 1000
	want := []int16{0, 500, 1000, 1000}
	for i := range want {
		if out.Samples[i] != want[i] {
			t.Fatalf("upsample[%d] = %d, want %d (all=%v)", i, out.Samples[i], want[i], out.Samples)
		}
	}

	// Downsample 48k -> 16k: 4 periods -> 1 output period (4*16/48).
	in = Frame{SampleRate: 48000, Channels: 1, Samples: []int16{100, 200, 300, 400}}
	out, st, err = Resample(in, 16000)
	if err != nil || st.OutputPeriods != 1 || out.Samples[0] != 100 {
		t.Fatalf("downsample wrong: %+v sample=%v err=%v", st, out.Samples, err)
	}
}

func TestResampleDeterministic(t *testing.T) {
	in := Frame{SampleRate: 44100, Channels: 2,
		Samples: []int16{120, -340, 560, -780, 900, -1024, 2048, -4096}}
	first, _, err := Resample(in, 48000)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := Resample(in, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Samples) != len(second.Samples) {
		t.Fatal("lengths differ between identical runs")
	}
	for i := range first.Samples {
		if first.Samples[i] != second.Samples[i] {
			t.Fatalf("non-deterministic at %d: %d vs %d", i, first.Samples[i], second.Samples[i])
		}
	}
}

func TestResampleShortFrameAndErrors(t *testing.T) {
	// One short source period still emits exactly one output period.
	in := Frame{SampleRate: 96000, Channels: 1, Samples: []int16{777}}
	out, st, err := Resample(in, 48000)
	if err != nil || st.OutputPeriods != 1 || out.Samples[0] != 777 {
		t.Fatalf("short frame wrong: %+v %v err=%v", st, out.Samples, err)
	}

	_, _, err = Resample(Frame{SampleRate: 48000, Channels: 2, Samples: []int16{1, 2, 3}}, 48000)
	if !model.HasCode(err, model.ErrUnsupportedLayout) {
		t.Fatalf("ragged frame: want UNSUPPORTED_LAYOUT, got %v", err)
	}
	_, _, err = Resample(Frame{SampleRate: 0, Channels: 1, Samples: []int16{1}}, 48000)
	if !model.HasCode(err, model.ErrTimestampOverflow) {
		t.Fatalf("bad rate: want TS_OVERFLOW, got %v", err)
	}
}
