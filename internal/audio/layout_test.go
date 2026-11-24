package audio

import (
	"testing"

	"mediad/internal/model"
)

func TestNormalizeMonoAndStereo(t *testing.T) {
	mono := Frame{SampleRate: 48000, Channels: 1, Samples: []int16{100, -200, 300}}
	out, st, err := NormalizeLayout(mono)
	if err != nil {
		t.Fatal(err)
	}
	if st.InputChannels != 1 || st.OutputChannels != 2 || st.DroppedSamples != 0 {
		t.Fatalf("stats wrong: %+v", st)
	}
	want := []int16{100, 100, -200, -200, 300, 300}
	for i := range want {
		if out.Samples[i] != want[i] {
			t.Fatalf("mono dup wrong at %d: got %d want %d", i, out.Samples[i], want[i])
		}
	}

	stereo := Frame{SampleRate: 48000, Channels: 2, Samples: []int16{1, 2, 3, 4}}
	out2, _, err := NormalizeLayout(stereo)
	if err != nil || len(out2.Samples) != 4 || out2.Samples[0] != 1 {
		t.Fatalf("stereo passthrough wrong: %+v err=%v", out2, err)
	}
}

func TestNormalizeDownmix51(t *testing.T) {
	// One 5.1 period: L=1000 R=2000 C=400 LFE=9999 BL=-800 BR=-1600.
	f := Frame{SampleRate: 48000, Channels: 6, Samples: []int16{
		1000, 2000, 400, 9999, -800, -1600,
	}}
	out, st, err := NormalizeLayout(f)
	if err != nil {
		t.Fatal(err)
	}
	if st.DroppedSamples != 0 || len(out.Samples) != 2 {
		t.Fatalf("stats/output wrong: %+v len=%d", st, len(out.Samples))
	}
	// L_out = 1000 + mix(400,7071) + mix(-800,7071)
	wantL := int16(1000 + mix(400, coefCenterNum) + mix(-800, coefSurroundNum))
	wantR := int16(2000 + mix(400, coefCenterNum) + mix(-1600, coefSurroundNum))
	if out.Samples[0] != wantL || out.Samples[1] != wantR {
		t.Fatalf("downmix = (%d,%d), want (%d,%d)", out.Samples[0], out.Samples[1], wantL, wantR)
	}
}

func TestNormalizeShortAndEmptyAndBadLayout(t *testing.T) {
	// 7 samples for a 6-channel layout: one complete period + 1 dropped.
	short := Frame{SampleRate: 48000, Channels: 6, Samples: []int16{
		10, 20, 30, 40, 50, 60, 99,
	}}
	out, st, err := NormalizeLayout(short)
	if err != nil || st.DroppedSamples != 1 || len(out.Samples) != 2 {
		t.Fatalf("short frame: err=%v stats=%+v out=%v", err, st, out.Samples)
	}

	_, _, err = NormalizeLayout(Frame{SampleRate: 48000, Channels: 2, Samples: nil})
	if !model.HasCode(err, model.ErrEmptyFrame) {
		t.Fatalf("empty: want EMPTY_FRAME, got %v", err)
	}
	_, _, err = NormalizeLayout(Frame{SampleRate: 48000, Channels: 3, Samples: []int16{1, 2, 3}})
	if !model.HasCode(err, model.ErrUnsupportedLayout) {
		t.Fatalf("3ch: want UNSUPPORTED_LAYOUT, got %v", err)
	}
	// Downmix clamps instead of overflowing.
	loud := make([]int16, 8)
	for i := range loud {
		loud[i] = 32767
	}
	out, _, err = NormalizeLayout(Frame{SampleRate: 48000, Channels: 8, Samples: loud})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range out.Samples {
		if s > 32767 || s < -32768 {
			t.Fatalf("clamp failed: %d", s)
		}
	}
}
