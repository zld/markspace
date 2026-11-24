package audio

import (
	"math"
	"testing"

	"mediapipe/internal/model"
)

func TestRationalRatios(t *testing.T) {
	cases := []struct {
		src, dst, up, down int64
	}{
		{48000, 96000, 2, 1},
		{96000, 48000, 1, 2},
		{44100, 48000, 160, 147},
		{48000, 44100, 147, 160},
		{48000, 48000, 1, 1},
	}
	for _, c := range cases {
		up, down, err := RationalRatio(c.src, c.dst)
		if err != nil || up != c.up || down != c.down {
			t.Errorf("%d->%d = %d/%d, want %d/%d (err=%v)", c.src, c.dst, up, down, c.up, c.down, err)
		}
	}
	if _, _, err := RationalRatio(22050, 48000); model.AsMediaError(err) == nil {
		t.Fatal("unsupported rate must be rejected")
	}
}

func TestResampleDCLevelPreservedAndDeterministic(t *testing.T) {
	in := make([]float64, 480) // 10 ms at 48k
	for i := range in {
		in[i] = 0.5
	}
	r1, err := Resample(in, 48000, 96000)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Samples) != 960 {
		t.Fatalf("output length = %d, want 960", len(r1.Samples))
	}
	// Interior samples (away from zero-padded edges) keep the DC level.
	for i := 32; i < 928; i++ {
		if math.Abs(r1.Samples[i]-0.5) > 1e-9 {
			t.Fatalf("DC not preserved at %d: %v", i, r1.Samples[i])
		}
	}
	r2, _ := Resample(in, 48000, 96000)
	for i := range r1.Samples {
		if r1.Samples[i] != r2.Samples[i] {
			t.Fatal("resampling not deterministic across runs")
		}
	}
}

func TestBoundaryZeroPaddingCounted(t *testing.T) {
	// Input shorter than one filter window forces deterministic zero pads.
	short := []float64{0.1, 0.2, 0.3}
	r, err := Resample(short, 44100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if r.ZeroPads == 0 || r.WindowSkips == 0 {
		t.Fatalf("boundary padding not accounted: %+v", r)
	}
	if len(r.Samples) != 3 { // round-half-up of 3 * 160/147 = 3.265...
		t.Fatalf("short input output length = %d, want 3", len(r.Samples))
	}
}

func TestDownsampleLength(t *testing.T) {
	in := make([]float64, 960) // 10 ms at 96k
	r, err := Resample(in, 96000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Samples) != 480 {
		t.Fatalf("downsampled length = %d, want 480", len(r.Samples))
	}
}

func TestStereoResampleKeepsChannelsAligned(t *testing.T) {
	pcm := []float64{0.2, -0.2, 0.4, -0.4, 0.6, -0.6}
	r, err := ResampleStereo(pcm, 48000, 96000)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Samples)%2 != 0 || len(r.Samples) != 12 {
		t.Fatalf("stereo output = %d samples", len(r.Samples))
	}
	if _, err := ResampleStereo([]float64{1, 2, 3}, 48000, 96000); model.AsMediaError(err) == nil {
		t.Fatal("odd stereo buffer must be rejected")
	}
}
