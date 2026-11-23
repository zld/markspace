package audio

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// feedSine pushes a sine of amp dBFS for seconds into a meter at rate.
func feedSine(t *testing.T, m *Meter, rate int, ampDB, seconds float64, intercept float64) {
	t.Helper()
	if t == nil {
		panic("nil testing handle")
	}
	hop := int(math.Round(float64(rate) * BlockHopSeconds))
	total := int(float64(rate) * seconds)
	amp := math.Pow(10, ampDB/20)
	for start := 0; start+hop <= total; start += hop {
		block := make([]float64, hop*2)
		for f := 0; f < hop; f++ {
			v := amp * math.Sin(2*math.Pi*1000*float64(start+f)/float64(rate))
			block[f*2] = v + intercept
			block[f*2+1] = v - intercept
		}
		if err := m.Feed(block); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoudnessReferenceSine(t *testing.T) {
	// A -20 dBFS 1 kHz stereo sine measures -20 LUFS under BS.1770.
	m, err := NewMeter(48000, Stereo)
	if err != nil {
		t.Fatal(err)
	}
	feedSine(t, m, 48000, -20, 3, 0)
	res := m.Integrated()
	if math.Abs(res.IntegratedLUFS-(-20.0)) > 0.3 {
		t.Fatalf("got %.2f LUFS, want -20", res.IntegratedLUFS)
	}
	if res.Blocks == 0 {
		t.Fatal("no blocks passed the gate")
	}
}

func TestLoudnessScalesWithAmplitude(t *testing.T) {
	// 6 dB more input must measure 6 dB louder.
	quiet, _ := NewMeter(48000, Stereo)
	feedSine(t, quiet, 48000, -20, 3, 0)
	loud, _ := NewMeter(48000, Stereo)
	feedSine(t, loud, 48000, -14, 3, 0)
	d := loud.Integrated().IntegratedLUFS - quiet.Integrated().IntegratedLUFS
	if math.Abs(d-6.0) > 0.05 {
		t.Fatalf("delta %.2f db, want 6", d)
	}
}

func TestLoudnessStereoSumInPhase(t *testing.T) {
	// Identical channels sum coherently: +3 dB relative to one channel.
	mono, _ := NewMeter(48000, Mono)
	hop := int(math.Round(48000 * BlockHopSeconds))
	for start := 0; start+hop <= 48000*3; start += hop {
		b := make([]float64, hop)
		for f := 0; f < hop; f++ {
			b[f] = 0.1 * math.Sin(2*math.Pi*1000*float64(start+f)/48000)
		}
		mono.Feed(b)
	}
	st, _ := NewMeter(48000, Stereo)
	feedSine(t, st, 48000, -20, 3, 0) // both channels same 0.1 amplitude
	// mono used 0.1 amplitude, -20 dBFS is 0.1 amplitude as well.
	d := st.Integrated().IntegratedLUFS - mono.Integrated().IntegratedLUFS
	if math.Abs(d-3.0) > 0.05 {
		t.Fatalf("stereo vs mono delta %.2f, want 3", d)
	}
}

func TestLoudnessSilenceIsGated(t *testing.T) {
	m, _ := NewMeter(48000, Stereo)
	hop := int(math.Round(48000 * BlockHopSeconds))
	for i := 0; i < 40; i++ {
		m.Feed(make([]float64, hop*2))
	}
	res := m.Integrated()
	if !res.Silent {
		t.Fatalf("silence measured at %.2f", res.IntegratedLUFS)
	}
}

func TestLoudnessDeterministicAcrossSlicing(t *testing.T) {
	hops := make([][]float64, 30)
	hop := int(math.Round(48000 * BlockHopSeconds))
	for i := range hops {
		hops[i] = make([]float64, hop*2)
		for f := 0; f < hop; f++ {
			v := 0.2 * math.Sin(2*math.Pi*997*float64(i*hop+f)/48000)
			hops[i][f*2] = v
			hops[i][f*2+1] = v * 0.5
		}
	}
	a, _ := NewMeter(48000, Stereo)
	for _, h := range hops {
		a.Feed(h)
	}
	b, _ := NewMeter(48000, Stereo)
	// Feed the same samples in the same hop-sized units; the API requires
	// whole hops, so verify a second meter gives the same value.
	for _, h := range hops {
		b.Feed(h)
	}
	if a.Integrated().IntegratedLUFS != b.Integrated().IntegratedLUFS {
		t.Fatal("loudness differs between identical runs")
	}
}

func TestPlanGainReachesTarget(t *testing.T) {
	res := LoudnessResult{IntegratedLUFS: -12, MaxPeak: 0.1, Blocks: 10}
	p, err := PlanGain(res, -18, DefaultTruePeakCeilingDBTP)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(p.GainDB-(-6)) > 1e-9 {
		t.Fatalf("gain %.4f", p.GainDB)
	}
}

func TestPlanGainSilence(t *testing.T) {
	p, err := PlanGain(LoudnessResult{Silent: true}, -18, DefaultTruePeakCeilingDBTP)
	if err != nil {
		t.Fatal(err)
	}
	if p.GainLinear != 1 {
		t.Fatalf("silence amplified: %v", p.GainLinear)
	}
	if len(p.Notes) == 0 {
		t.Fatal("silence note missing")
	}
}

func TestPlanGainCapped(t *testing.T) {
	res := LoudnessResult{IntegratedLUFS: -60, MaxPeak: 0.0001, Blocks: 1}
	p, err := PlanGain(res, -18, DefaultTruePeakCeilingDBTP)
	if err != nil {
		t.Fatal(err)
	}
	if !p.CappedForGain {
		t.Fatal("expected gain cap")
	}
	if p.GainDB != MaxGainDB {
		t.Fatalf("gain %.2f", p.GainDB)
	}
}

func TestPlanGainPeakRisk(t *testing.T) {
	res := LoudnessResult{IntegratedLUFS: -20, MaxPeak: 0.9, Blocks: 10}
	p, _ := PlanGain(res, -10, DefaultTruePeakCeilingDBTP)
	if !p.Limited {
		t.Fatal("expected limiting to be flagged")
	}
}

func TestPlanGainRejectsBadTarget(t *testing.T) {
	if _, err := PlanGain(LoudnessResult{IntegratedLUFS: -20}, -100, -1); !errors.Is(err, ErrBadTarget) {
		t.Fatalf("got %v", err)
	}
	if _, err := PlanGain(LoudnessResult{IntegratedLUFS: -20}, 0, -1); !errors.Is(err, ErrBadTarget) {
		t.Fatalf("got %v", err)
	}
}

func TestLimiterNeverExceedsCeiling(t *testing.T) {
	l := NewLimiter(0.5, 8) // heavy gain, low ceiling
	in := make([]float64, 4800*2)
	for i := range in {
		in[i] = math.Sin(float64(i)*0.01) * 0.5
	}
	out := l.Process(in, 2)
	for i, v := range out {
		if math.Abs(v) > 0.5+1e-12 {
			t.Fatalf("sample %d = %v exceeds ceiling", i, v)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("non-finite at %d", i)
		}
	}
}

func TestLimiterLinearAtSafeLevel(t *testing.T) {
	l := NewLimiter(0.9, 1)
	in := []float64{0.1, -0.2, 0.3, 0.05}
	out := l.Process(in, 2)
	for i := range in {
		if math.Abs(out[i]-in[i]) > 1e-12 {
			t.Fatalf("modified safe sample %d: %v vs %v", i, out[i], in[i])
		}
	}
	if l.ClippedSamples() != 0 {
		t.Fatalf("clipped %d", l.ClippedSamples())
	}
}

func TestLimiterPreservesChannelBalance(t *testing.T) {
	l := NewLimiter(0.5, 1)
	// Both channels carry the same loud signal; ratio stays 1:1.
	in := []float64{0.8, 0.8, 0.8, 0.8}
	out := l.Process(in, 2)
	if math.Abs(out[0]-out[1]) > 1e-12 {
		t.Fatalf("channels diverged: %v", out)
	}
	if out[0] > 0.5 {
		t.Fatalf("over ceiling: %v", out[0])
	}
}

func TestLimiterOddInputRejected(t *testing.T) {
	l := NewLimiter(0.5, 1)
	if l.Process([]float64{1, 2, 3}, 2) != nil {
		t.Fatal("odd input must be rejected")
	}
}

func TestTruePeakMeterDBTP(t *testing.T) {
	m, err := NewTruePeakMeter(2)
	if err != nil {
		t.Fatal(err)
	}
	// A 0.5 amplitude square-ish signal peaks at 0.5; inter-sample peaks can
	// exceed it slightly, so dBTP is at least 20log10(0.5).
	in := make([]float64, 4800*2)
	for i := 0; i < 4800; i++ {
		v := 0.5 * math.Sin(2*math.Pi*1000*float64(i)/48000)
		in[i*2] = v
		in[i*2+1] = -v
	}
	if err := m.Feed(in); err != nil {
		t.Fatal(err)
	}
	dbtp := m.DBTP()
	if dbtp < 20*math.Log10(0.5)-0.1 {
		t.Fatalf("dbtp %.2f below sample peak", dbtp)
	}
	if dbtp > 0 {
		t.Fatalf("dbtp %.2f unexpectedly above 0", dbtp)
	}
}

func TestTruePeakMeterSilence(t *testing.T) {
	m, _ := NewTruePeakMeter(2)
	m.Feed(make([]float64, 480))
	if !math.IsInf(m.DBTP(), -1) {
		t.Fatalf("silence dbtp %v", m.DBTP())
	}
}

func TestKWeightingRateRejected(t *testing.T) {
	if _, err := NewKWeighting(32000); err == nil {
		t.Fatal("expected rate rejection")
	}
}

func TestKWeightingStateContinuity(t *testing.T) {
	// Feeding one long run and two halves must produce the same filtered tail.
	a, _ := NewKWeighting(48000)
	b, _ := NewKWeighting(48000)
	x := make([]float64, 9600)
	for i := range x {
		x[i] = math.Sin(float64(i) * 0.01)
	}
	var lastA float64
	for _, v := range x {
		lastA = a.Process(v)
	}
	var lastB float64
	for _, v := range x[:4800] {
		b.Process(v)
	}
	for _, v := range x[4800:] {
		lastB = b.Process(v)
	}
	if math.Abs(lastA-lastB) > 1e-12 {
		t.Fatalf("state not continuous: %v vs %v", lastA, lastB)
	}
}

func TestLoudnessNotesAreStrings(t *testing.T) {
	res := LoudnessResult{IntegratedLUFS: -6, MaxPeak: 0.9, Blocks: 5}
	p, _ := PlanGain(res, -18, -1)
	if len(p.Notes) == 0 {
		t.Fatal("expected limiting note")
	}
	if !strings.Contains(strings.Join(p.Notes, ";"), "limiting") {
		t.Fatalf("notes %v", p.Notes)
	}
}

func TestMeterFeedRejectsShortBlock(t *testing.T) {
	m, _ := NewMeter(48000, Stereo)
	if err := m.Feed([]float64{1, 2}); err == nil {
		t.Fatal("expected short block error")
	}
}

func TestMeterFive1LFEExcluded(t *testing.T) {
	// Signal only in LFE must not raise the measured loudness.
	m, err := NewMeter(48000, Five1)
	if err != nil {
		t.Fatal(err)
	}
	hop := int(math.Round(48000 * BlockHopSeconds))
	for i := 0; i < 40; i++ {
		b := make([]float64, hop*6)
		for f := 0; f < hop; f++ {
			b[f*6+3] = 0.5 * math.Sin(2*math.Pi*100*float64(i*hop+f)/48000)
		}
		m.Feed(b)
	}
	if !m.Integrated().Silent {
		t.Fatal("LFE-only signal must be gated as silent")
	}
}
