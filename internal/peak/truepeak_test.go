package peak

import (
	"math"
	"testing"
)

func run(t *testing.T, channels int, frames []float64) Result {
	t.Helper()
	m, err := NewMeter(channels)
	if err != nil {
		t.Fatalf("NewMeter: %v", err)
	}
	if err := m.Process(frames); err != nil {
		t.Fatalf("Process: %v", err)
	}
	return m.Finish()
}

func sine(sampleRate, channels int, seconds, amp, freq float64) []float64 {
	n := int(math.Round(seconds * float64(sampleRate)))
	out := make([]float64, n*channels)
	for i := 0; i < n; i++ {
		v := amp * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate))
		for c := 0; c < channels; c++ {
			out[i*channels+c] = v
		}
	}
	return out
}

func TestFlatPeakIsSamplePeak(t *testing.T) {
	frames := make([]float64, 1000*2)
	for i := range frames {
		frames[i] = 0.5
	}
	res := run(t, 2, frames)
	if math.Abs(res.DBTP-20*math.Log10(0.5)) > 1e-9 {
		t.Errorf("dBTP = %f, want %f", res.DBTP, 20*math.Log10(0.5))
	}
}

func TestIntersampleOvershootDetected(t *testing.T) {
	// A quarter-rate sine sampled at 0 degrees has every sample at zero, but
	// the reconstructed waveform reaches full amplitude between samples.
	sampleRate := 48000
	freq := float64(sampleRate) / 4
	n := 4000
	frames := make([]float64, n)
	for i := 0; i < n; i++ {
		// Phase offset of pi/4 puts every sample at 0.7071 of the crest, so a
		// sample-peak scan reports -3.01 dBFS while the waveform reaches 0 dBFS
		// between samples.
		frames[i] = math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate) + math.Pi/4)
	}
	res := run(t, 1, frames)
	if math.Abs(res.SamplePeak-1/math.Sqrt2) > 0.01 {
		t.Fatalf("sample peak = %.4f, want 0.7071", res.SamplePeak)
	}
	if math.Abs(res.DBTP) > 0.1 {
		t.Errorf("dBTP = %.3f, want about 0 (inter-sample overshoot missed)", res.DBTP)
	}
}

func TestHalfAmplitudeQuarterRate(t *testing.T) {
	// 0.5 amplitude at fs/4 phased to avoid samples: the true peak is 0.5,
	// i.e. -6.02 dBTP, which a naive sample-peak scan would report as -inf.
	sampleRate := 48000
	frames := sine(sampleRate, 1, 0.2, 0.5, float64(sampleRate)/4)
	// Shift by half a sample so samples never land on the crest.
	shifted := make([]float64, len(frames))
	for i := range frames {
		shifted[i] = 0.5 * math.Sin(2*math.Pi*(float64(i)+0.5)/4)
	}
	res := run(t, 1, shifted)
	if math.Abs(res.DBTP-20*math.Log10(0.5)) > 0.1 {
		t.Errorf("dBTP = %.3f, want %.3f", res.DBTP, 20*math.Log10(0.5))
	}
}

func TestAboveFullScalePreserved(t *testing.T) {
	// Values above 1.0 must be reported, not clamped.
	frames := make([]float64, 100)
	for i := range frames {
		frames[i] = 1.5
	}
	res := run(t, 1, frames)
	if res.Linear < 1.5 || res.DBTP < 3.5 {
		t.Errorf("peak = %v, dBTP = %v, want >= 1.5 / >= 3.5", res.Linear, res.DBTP)
	}
}

func TestChunkedMatchesWhole(t *testing.T) {
	frames := sine(48000, 2, 0.1, 0.8, 997)
	whole := run(t, 2, frames)

	m, _ := NewMeter(2)
	// Odd frame counts, never a multiple of the filter window.
	for start := 0; start < len(frames); start += 7 * 2 {
		end := start + 7*2
		if end > len(frames) {
			end = len(frames)
		}
		if err := m.Process(frames[start:end]); err != nil {
			t.Fatal(err)
		}
	}
	chunked := m.Finish()
	if math.Abs(chunked.DBTP-whole.DBTP) > 1e-9 {
		t.Errorf("chunked %.6f vs whole %.6f", chunked.DBTP, whole.DBTP)
	}
}

func TestSilenceIsNegativeInfinity(t *testing.T) {
	res := run(t, 2, make([]float64, 2000))
	if !math.IsInf(res.DBTP, -1) {
		t.Errorf("dBTP = %v, want -Inf", res.DBTP)
	}
	if res.Channel != -1 {
		t.Errorf("channel = %d, want -1", res.Channel)
	}
}

func TestNonFiniteRejected(t *testing.T) {
	m, _ := NewMeter(1)
	if err := m.Process([]float64{math.NaN()}); err == nil {
		t.Fatal("NaN accepted")
	}
	m, _ = NewMeter(1)
	if err := m.Process([]float64{math.Inf(-1)}); err == nil {
		t.Fatal("Inf accepted")
	}
}

func TestChannelCountValidation(t *testing.T) {
	if _, err := NewMeter(0); err == nil {
		t.Error("zero channels accepted")
	}
	if _, err := NewMeter(9); err == nil {
		t.Error("nine channels accepted")
	}
}

func TestPeakChannelReported(t *testing.T) {
	frames := make([]float64, 1000*2)
	for i := 0; i < len(frames); i += 2 {
		frames[i] = 0.1
		frames[i+1] = 0.9
	}
	res := run(t, 2, frames)
	if res.Channel != 1 {
		t.Errorf("channel = %d, want 1", res.Channel)
	}
}

func TestKernelUnityGain(t *testing.T) {
	for phase := 1; phase < oversampleFactor; phase++ {
		var sum float64
		for _, v := range kernel[phase] {
			sum += v
		}
		if math.Abs(sum-1) > 1e-12 {
			t.Errorf("phase %d gain = %.15f", phase, sum)
		}
	}
}
