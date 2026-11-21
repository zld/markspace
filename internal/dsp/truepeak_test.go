package dsp

import (
	"math"
	"testing"
)

func truePeakOf(sig []float64) (sample, peak float64) {
	tp := NewTruePeak()
	tp.Write(sig)
	tp.Finish()
	return tp.SamplePeak(), tp.Peak()
}

func sine(n int, freq float64, amp float64, phase float64, fs float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = amp * math.Sin(2*math.Pi*freq*float64(i)/fs+phase)
	}
	return out
}

// fs/4 with a 45 degree start gives samples at +-0.707 of the analog peak,
// so a 4x interpolator must recover the missing 3.01 dB.
func TestTruePeakRecoversMissedCrest(t *testing.T) {
	const fs = 48000
	sig := sine(4800, fs/4, 0.5, math.Pi/4, fs)
	sample, peak := truePeakOf(sig)
	wantSample := 0.5 * math.Cos(math.Pi/4)
	if math.Abs(sample-wantSample) > 1e-9 {
		t.Fatalf("sample peak = %v, want %v", sample, wantSample)
	}
	if got, want := 20*math.Log10(peak), 20*math.Log10(0.5); math.Abs(got-want) > 0.05 {
		t.Errorf("true peak = %.4f dB, want %.4f dB", got, want)
	}
}

// For steady tones the interpolated peak must not overshoot the analog
// amplitude by a meaningful amount anywhere away from the 8-sample rim.
func TestTruePeakDoesNotOvershootTones(t *testing.T) {
	const fs = 48000
	for _, f := range []float64{100, 997, 5000, 10000, 15000} {
		for p := 0; p < 8; p++ {
			sig := sine(48000, f, 0.5, float64(p)*math.Pi/8, fs)
			_, peak := truePeakOf(sig)
			db := 20 * math.Log10(peak/0.5)
			if db > 0.15 {
				t.Errorf("%.0f Hz phase %d: true peak %.3f dB above the tone", f, p, db)
			}
			if db < -0.15 {
				t.Errorf("%.0f Hz phase %d: true peak %.3f dB below the tone", f, p, db)
			}
		}
	}
}

func TestTruePeakNeverBelowSamplePeak(t *testing.T) {
	sig := make([]float64, 5000)
	for i := range sig {
		// Sparse impulses at irregular intervals.
		if i%97 == 0 {
			sig[i] = 0.9 - 0.001*float64(i%13)
		}
	}
	sample, peak := truePeakOf(sig)
	if peak < sample {
		t.Fatalf("peak %v below sample peak %v", peak, sample)
	}
}

func TestTruePeakSilenceIsZero(t *testing.T) {
	_, peak := truePeakOf(make([]float64, 1000))
	if peak != 0 {
		t.Fatalf("silence peak = %v, want 0", peak)
	}
}

func TestTruePeakHandlesShortInputs(t *testing.T) {
	for n := 0; n <= 4*interpTaps; n++ {
		sig := make([]float64, n)
		if n > 0 {
			sig[n/2] = 0.75
		}
		sample, peak := truePeakOf(sig)
		if n == 0 {
			if sample != 0 || peak != 0 {
				t.Errorf("empty input: sample=%v peak=%v", sample, peak)
			}
			continue
		}
		if math.Abs(sample-0.75) > 1e-12 {
			t.Errorf("%d samples: sample peak = %v", n, sample)
		}
		if peak < 0.75 {
			t.Errorf("%d samples: peak %v dropped below the raw sample", n, peak)
		}
	}
}

// Writing in different block sizes must not change the result: the detector
// only looks one kernel ahead and one back, so any partition of the stream
// produces the same numbers.
func TestTruePeakBlockSizeIndependence(t *testing.T) {
	const fs = 48000
	sig := sine(20000, 11025, 0.8, 0.3, fs)
	wantSample, wantPeak := truePeakOf(sig)

	for _, block := range []int{1, 3, 17, 1000, 9999} {
		tp := NewTruePeak()
		for i := 0; i < len(sig); i += block {
			end := i + block
			if end > len(sig) {
				end = len(sig)
			}
			tp.Write(sig[i:end])
		}
		tp.Finish()
		if math.Abs(tp.SamplePeak()-wantSample) > 1e-12 {
			t.Errorf("block %d: sample peak %v != %v", block, tp.SamplePeak(), wantSample)
		}
		if math.Abs(tp.Peak()-wantPeak) > 1e-12 {
			t.Errorf("block %d: peak %v != %v", block, tp.Peak(), wantPeak)
		}
	}
}

// History is pruned as the stream advances; memory must stay bounded for a
// long file instead of growing with duration.
func TestTruePeakMemoryIsBounded(t *testing.T) {
	tp := NewTruePeak()
	block := sine(4096, 997, 0.5, 0, 48000)
	for i := 0; i < 500; i++ {
		tp.Write(block)
		if len(tp.buf) > 4*interpTaps+8192 {
			t.Fatalf("buffer grew to %d samples after %d blocks", len(tp.buf), i)
		}
	}
	tp.Finish()
}

func TestTruePeakAfterFinishIgnoresWrites(t *testing.T) {
	tp := NewTruePeak()
	tp.Write([]float64{0.5, 0.5, 0.5, 0.5})
	tp.Finish()
	before := tp.Peak()
	for i := 0; i < 100; i++ {
		tp.Write([]float64{1})
	}
	if tp.Peak() != before {
		t.Fatalf("a write after Finish changed the result: %v -> %v", before, tp.Peak())
	}
}

func TestBesselI0(t *testing.T) {
	for _, tc := range []struct{ x, want float64 }{
		{0, 1}, {1, 1.2660658777520084}, {5, 27.239871823604442},
	} {
		if got := besselI0(tc.x); math.Abs(got-tc.want) > 1e-9*tc.want {
			t.Errorf("I0(%v) = %v, want %v", tc.x, got, tc.want)
		}
	}
}

func TestPhasesHaveUnityDCGain(t *testing.T) {
	for p, row := range truePeakPhases {
		sum := 0.0
		for _, v := range row {
			sum += v
		}
		if math.Abs(sum-1) > 1e-12 {
			t.Errorf("phase %d sums to %v, want 1", p, sum)
		}
		if len(row) != 2*interpTaps+1 {
			t.Errorf("phase %d has %d taps, want %d", p, len(row), 2*interpTaps+1)
		}
	}
}
