package loudness

import (
	"math"
	"testing"

	"loudcheck/internal/fixture"
)

const testRate = 48000

// analyzeTone renders a tone through the fixture encoder's signal path and
// measures it with the given per-channel weights.
func analyzeTone(t *testing.T, seconds float64, dbfs float64, weights []float64) Result {
	t.Helper()
	opts := fixture.Options{
		SampleRate: testRate,
		Channels:   len(weights),
		Seconds:    seconds,
		Encoding:   fixture.Float32,
		ToneDBFS:   dbfs,
		ToneHz:     997,
	}
	sig := fixture.Signal(opts)
	an := NewAnalyzer(testRate, weights)
	buf := make([]float32, len(sig))
	for i, v := range sig {
		buf[i] = float32(v)
	}
	an.Write(buf)
	return an.Finish()
}

// analyzeToneChannels is analyzeTone with explicit tone channels, so a test
// can put signal on the LFE channel and confirm it is ignored.
func analyzeToneChannels(t *testing.T, seconds, dbfs float64, weights []float64, toneCh []int) Result {
	t.Helper()
	opts := fixture.Options{
		SampleRate: testRate, Channels: len(weights), Seconds: seconds,
		Encoding: fixture.Float32, ToneDBFS: dbfs, ToneHz: 997,
		ToneChannels: toneCh,
	}
	sig := fixture.Signal(opts)
	buf := make([]float32, len(sig))
	for i, v := range sig {
		buf[i] = float32(v)
	}
	an := NewAnalyzer(testRate, weights)
	an.Write(buf)
	return an.Finish()
}

func near(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

// A single full-scale 997 Hz sine measures -3.01 LUFS per BS.1770. Two
// uncorrelated channels add 3.01 LU, and a channel at half amplitude subtracts
// 6.02 LU.
func TestIntegratedLoudnessCalibration(t *testing.T) {
	mono := analyzeTone(t, 5, -23, []float64{1})
	if !mono.IntegratedValid {
		t.Fatal("mono measurement should be valid")
	}
	// -23 dBFS sine: -23 + (-3.01) = -26.01 LUFS.
	if !near(mono.IntegratedLUFS, -26.01, 0.15) {
		t.Errorf("mono integrated = %.3f, want about -26.01", mono.IntegratedLUFS)
	}

	// Two identical channels: +3.01 LU.
	stereo := analyzeTone(t, 5, -23, []float64{1, 1})
	if !near(stereo.IntegratedLUFS, mono.IntegratedLUFS+3.01, 0.1) {
		t.Errorf("stereo integrated = %.3f, want %.3f", stereo.IntegratedLUFS, mono.IntegratedLUFS+3.01)
	}

	// The LFE channel carries a full-weight tone, but its weight is zero, so
	// it must not contribute. Comparing against the same layout with a
	// silent LFE proves the exclusion rather than just repeating the run.
	withLFE := analyzeToneChannels(t, 5, -23, []float64{1, 1, 1, 0, 1.41, 1.41}, []int{0, 1, 2, 3, 4, 5})
	withoutLFE := analyzeToneChannels(t, 5, -23, []float64{1, 1, 1, 0, 1.41, 1.41}, []int{0, 1, 2, 4, 5})
	if !near(withLFE.IntegratedLUFS, withoutLFE.IntegratedLUFS, 1e-9) {
		t.Errorf("LFE channel changed the result: %.9f vs %.9f",
			withLFE.IntegratedLUFS, withoutLFE.IntegratedLUFS)
	}
}

func TestSurroundWeight(t *testing.T) {
	// Front-only versus surround-only at the same level should differ by
	// 10*log10(1.41) = 1.49 LU, because loudness sums channel power and the
	// result is expressed on a power-based log scale.
	front := analyzeTone(t, 5, -23, []float64{1, 0})
	surround := analyzeTone(t, 5, -23, []float64{0, surroundWeight})
	if !near(surround.IntegratedLUFS-front.IntegratedLUFS, 10*math.Log10(surroundWeight), 0.05) {
		t.Errorf("front %.3f surround %.3f, want a 1.49 LU difference",
			front.IntegratedLUFS, surround.IntegratedLUFS)
	}
}

func TestSilenceIsInvalid(t *testing.T) {
	res := analyzeTone(t, 5, -999, []float64{1})
	if res.IntegratedValid {
		t.Fatalf("silence reported a valid integrated loudness: %v", res.IntegratedLUFS)
	}
	if res.GatedBlockCount != 0 {
		t.Errorf("gated blocks = %d, want 0", res.GatedBlockCount)
	}
	if res.TruePeak != 0 || res.SamplePeak != 0 {
		t.Errorf("silence peak = %v / %v", res.TruePeak, res.SamplePeak)
	}
}

func TestTooShortForGating(t *testing.T) {
	// 300 ms cannot form a 400 ms block, so loudness must be reported as
	// invalid rather than extrapolated from a partial window.
	res := analyzeTone(t, 0.3, -23, []float64{1})
	if res.IntegratedValid || res.TotalBlockCount != 0 {
		t.Fatalf("300 ms produced %d blocks / valid=%v", res.TotalBlockCount, res.IntegratedValid)
	}
	if !res.ShortTermInsufficient {
		t.Error("300 ms should be flagged as too short for a short-term window")
	}
	if res.ShortTermValid {
		t.Error("short-term value must not be valid below 3 s")
	}
}

func TestShortTermWindowNeedsThreeSeconds(t *testing.T) {
	just := analyzeTone(t, 2.95, -23, []float64{1})
	if !just.ShortTermInsufficient || just.ShortTermValid {
		t.Errorf("2.95 s should not produce a short-term value")
	}
	if !just.IntegratedValid {
		t.Error("2.95 s is long enough for integrated loudness")
	}
	enough := analyzeTone(t, 3.05, -23, []float64{1})
	if !enough.ShortTermValid || enough.ShortTermInsufficient {
		t.Errorf("3.05 s should produce a short-term value")
	}
	if !near(enough.ShortTermPeakLUFS, enough.IntegratedLUFS, 0.1) {
		t.Errorf("steady tone: short-term %.2f vs integrated %.2f",
			enough.ShortTermPeakLUFS, enough.IntegratedLUFS)
	}
}

// A quiet passage plus a loud passage must be dominated by the loud one: the
// relative gate removes blocks more than 10 LU below the ungated mean.
// With a 30 dB step the quiet half sits ~33 LU down, well outside the gate.
func TestRelativeGate(t *testing.T) {
	opts := fixture.Options{
		SampleRate: testRate, Channels: 1, Seconds: 10,
		Encoding: fixture.Float32, ToneDBFS: -23, ToneHz: 997,
	}
	sig := fixture.Signal(opts)
	// First 5 s are 30 dB quieter.
	for i := 0; i < len(sig)/2; i++ {
		sig[i] *= 0.0316
	}
	an := NewAnalyzer(testRate, []float64{1})
	buf := make([]float32, len(sig))
	for i, v := range sig {
		buf[i] = float32(v)
	}
	an.Write(buf)
	res := an.Finish()
	if !res.IntegratedValid {
		t.Fatal("mixed signal should be measurable")
	}
	loud := analyzeTone(t, 5, -23, []float64{1})
	if !near(res.IntegratedLUFS, loud.IntegratedLUFS, 0.2) {
		t.Errorf("gated result %.2f should match the loud section %.2f",
			res.IntegratedLUFS, loud.IntegratedLUFS)
	}
	if res.GatedBlockCount >= res.TotalBlockCount {
		t.Errorf("gated %d of %d blocks: quiet blocks should have been removed",
			res.GatedBlockCount, res.TotalBlockCount)
	}
}

func TestAbsoluteGateRemovesNearSilence(t *testing.T) {
	opts := fixture.Options{
		SampleRate: testRate, Channels: 1, Seconds: 10,
		Encoding: fixture.Float32, ToneDBFS: -23, ToneHz: 997,
	}
	sig := fixture.Signal(opts)
	// Make the first half -80 dBFS: below the -70 LUFS absolute gate.
	for i := 0; i < len(sig)/2; i++ {
		sig[i] *= math.Pow(10, -57.0/20)
	}
	an := NewAnalyzer(testRate, []float64{1})
	buf := make([]float32, len(sig))
	for i, v := range sig {
		buf[i] = float32(v)
	}
	an.Write(buf)
	res := an.Finish()
	loud := analyzeTone(t, 5, -23, []float64{1})
	if !near(res.IntegratedLUFS, loud.IntegratedLUFS, 0.2) {
		t.Errorf("absolute gating result %.2f, want about %.2f", res.IntegratedLUFS, loud.IntegratedLUFS)
	}
}

// Feeding a file in many small blocks must produce exactly the same numbers
// as feeding it in one block.
func TestBlockSizeIndependence(t *testing.T) {
	opts := fixture.Options{
		SampleRate: testRate, Channels: 2, Seconds: 6,
		Encoding: fixture.Float32, ToneDBFS: -20, ToneHz: 997,
	}
	sig := fixture.Signal(opts)
	interleaved := make([]float32, len(sig))
	for i, v := range sig {
		interleaved[i] = float32(v)
	}

	whole := NewAnalyzer(testRate, []float64{1, 1})
	whole.Write(interleaved)
	want := whole.Finish()

	chunked := NewAnalyzer(testRate, []float64{1, 1})
	const step = 1001 * 2
	for i := 0; i < len(interleaved); i += step {
		end := i + step
		if end > len(interleaved) {
			end = len(interleaved)
		}
		chunked.Write(interleaved[i:end])
	}
	got := chunked.Finish()

	if math.Abs(got.IntegratedLUFS-want.IntegratedLUFS) > 1e-9 {
		t.Errorf("chunked integrated %.12f != whole %.12f", got.IntegratedLUFS, want.IntegratedLUFS)
	}
	if got.GatedBlockCount != want.GatedBlockCount {
		t.Errorf("chunked gated blocks %d != %d", got.GatedBlockCount, want.GatedBlockCount)
	}
	if math.Abs(got.TruePeak-want.TruePeak) > 1e-9 {
		t.Errorf("chunked true peak %v != %v", got.TruePeak, want.TruePeak)
	}
}
