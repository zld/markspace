package audio

import (
	"math"
	"testing"
)

func tone(rate, n int, freq float64, amp int16) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(float64(amp) * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

func TestResampleRatiosAndTail(t *testing.T) {
	cases := []struct {
		name             string
		src, dst, frames int
		wantOut          int
	}{
		{"48k-to-24k half", 48000, 24000, BlockSize, BlockSize / 2},
		{"24k-to-48k double", 24000, 48000, BlockSize, BlockSize * 2},
		{"48k-to-44.1k frac", 48000, 44100, BlockSize, 441},
		{"same rate identity", 48000, 48000, BlockSize, BlockSize},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := NewResampler(c.src, c.dst)
			if err != nil {
				t.Fatalf("new: %v", err)
			}
			in := tone(c.src, c.frames, 1000, 10000)
			out := r.ResampleBlock(in)
			if len(out) != c.wantOut {
				t.Fatalf("block out = %d, want %d", len(out), c.wantOut)
			}
			if r.Carried() != 0 {
				t.Fatalf("carry = %d, want 0", r.Carried())
			}
		})
	}

	// Short tail: fewer frames than one conversion window must be retained,
	// then flushed with a partial-boundary marker and no out-of-range read.
	r, _ := NewResampler(48000, 24000)
	if out := r.ResampleBlock(tone(48000, 100, 1000, 8000)); out != nil {
		t.Fatalf("partial input emitted %d frames", len(out))
	}
	if r.Carried() != 100 {
		t.Fatalf("carry = %d, want 100", r.Carried())
	}
	tail, partial := r.Flush()
	if !partial || len(tail) != 50 {
		t.Fatalf("flush = %d frames partial=%v, want 50/true", len(tail), partial)
	}
}

func TestResampleDeterministic(t *testing.T) {
	in := tone(48000, BlockSize*3, 1234.5, 12000)
	run := func() []int16 {
		r, _ := NewResampler(48000, 44100)
		var out []int16
		for i := 0; i < len(in); i += BlockSize {
			out = append(out, r.ResampleBlock(in[i:i+BlockSize])...)
		}
		return out
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("lengths differ %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at %d: %d vs %d", i, a[i], b[i])
		}
	}
}

func TestLayoutNormalization(t *testing.T) {
	stereo := []int16{100, 200, 300, 400} // two frames L/R
	mono := NormalizeLayout(stereo, 2, 1)
	if len(mono) != 2 || mono[0] != 150 || mono[1] != 350 {
		t.Fatalf("downmix = %v", mono)
	}
	back := NormalizeLayout(mono, 1, 2)
	if len(back) != 4 || back[0] != 150 || back[1] != 150 {
		t.Fatalf("upmix = %v", back)
	}
	// Odd trailing samples never cause an out-of-range frame read.
	odd := NormalizeLayout([]int16{1, 2, 3}, 2, 1)
	if len(odd) != 1 || odd[0] != 1 {
		t.Fatalf("odd tail = %v, want [1]", odd)
	}
}

func TestLoudnessTargetAndLimiter(t *testing.T) {
	// A near-full-scale tone pushed toward an even louder target requires
	// positive gain; its peaks cross full scale and the limiter pulls them
	// back to the hard 0 dBFS ceiling.
	in := tone(48000, 4800, 440, 30000)
	st, err := ApplyLoudness(in, -1)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if st.Clipped == 0 {
		t.Fatalf("expected limiter activity, clipped=%d", st.Clipped)
	}
	if st.PeakAfter > 0.0001 { // hard ceiling at 0 dBFS
		t.Fatalf("peak after = %f dBFS exceeds ceiling", st.PeakAfter)
	}
	// dBFS is negative scale: the limited peak (near 0) must be louder than
	// or equal to the pre-limit peak, never above the ceiling. This before/after
	// pair is the clipping evidence carried in the report.
	if st.PeakAfter < st.PeakBefore-0.0001 {
		t.Fatalf("limiter reduced peak below original: before=%f after=%f",
			st.PeakBefore, st.PeakAfter)
	}
}

func TestLoudnessSilenceNoInfiniteGain(t *testing.T) {
	silence := make([]int16, 1000)
	st, err := ApplyLoudness(silence, -16)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !math.IsInf(st.MeasuredLUFS, 0) {
		t.Fatalf("silence LUFS = %f, want -Inf", st.MeasuredLUFS)
	}
	if st.GainDB != 0 {
		t.Fatalf("silence gain = %f dB, want 0 (no infinite gain)", st.GainDB)
	}
	for _, s := range silence {
		if s != 0 {
			t.Fatalf("silence samples were modified: %d", s)
		}
	}
}

func TestMeasureAndPeak(t *testing.T) {
	if _, err := MeasureLUFS(nil); err != ErrNoSamples {
		t.Fatalf("empty measure err = %v", err)
	}
	sq := []int16{16384, -16384}
	lufs, err := MeasureLUFS(sq)
	if err != nil || lufs >= 0 {
		t.Fatalf("lufs = %f err = %v", lufs, err)
	}
	peak, _ := PeakDBFS([]int16{0, -32768, 100})
	want := dbFS(1.0) // |-32768| / 32768
	if math.Abs(peak-want) > 1e-9 {
		t.Fatalf("peak = %f, want %f", peak, want)
	}
}
