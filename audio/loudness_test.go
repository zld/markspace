package audio

import (
	"math"
	"testing"
)

func fullScaleTone(n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * 32767 * math.Sin(2*math.Pi*float64(i)/8))
	}
	return out
}

func TestLoudnessChunkedEqualsOneShot(t *testing.T) {
	in := fullScaleTone(4000, 0.5)
	one := NewLoudnessMeter(1)
	one.Accumulate(in)

	chunked := NewLoudnessMeter(1)
	for start := 0; start < len(in); start += 333 {
		end := start + 333
		if end > len(in) {
			end = len(in)
		}
		chunked.Accumulate(in[start:end])
	}
	if math.Abs(one.LUFS()-chunked.LUFS()) > 1e-12 {
		t.Fatalf("LUFS diverges: one=%v chunked=%v", one.LUFS(), chunked.LUFS())
	}
}

func TestLoudnessTargetGainReachesMinusSixteen(t *testing.T) {
	// A tone 6 dB too quiet should request roughly +6 dB of gain.
	in := fullScaleTone(8000, 0.25)
	m := NewLoudnessMeter(1)
	m.Accumulate(in)
	before := m.LUFS()
	gainDB := m.ComputeGain()
	m.Apply(in)
	// re-measure the processed signal
	after := NewLoudnessMeter(1)
	after.Accumulate(m.Apply(in))
	if math.Abs(after.LUFS()-TargetLUFS) > 0.5 {
		t.Fatalf("processed LUFS %.2f not near target %.1f (was %.2f, gain %.2f dB)",
			after.LUFS(), TargetLUFS, before, gainDB)
	}
}

func TestLoudnessPeakNeverExceedsCeiling(t *testing.T) {
	in := fullScaleTone(4000, 1.0)
	m := NewLoudnessMeter(1)
	m.Accumulate(in)
	m.ComputeGain()
	out := m.Apply(in)
	peak := TruePeakDBFS(out)
	if peak > PeakCeilingDBFS+1e-9 {
		t.Fatalf("true peak %.3f dBFS exceeds ceiling %.3f", peak, PeakCeilingDBFS)
	}
}

func TestLoudnessSilenceGainBounded(t *testing.T) {
	m := NewLoudnessMeter(2)
	m.Accumulate(make([]int16, 100))
	g := m.TargetGainDB()
	if g != MaxGainDB {
		t.Fatalf("silence gain %v should be clamped to %v", g, MaxGainDB)
	}
	if math.IsInf(m.LUFS(), 0) == false {
		t.Fatal("silence must report -Inf LUFS")
	}
}

func TestLoudnessRoundingRuleStable(t *testing.T) {
	// ties away from zero: +/-0.5 round to +/-1
	if quantize16(0.5) != 1 || quantize16(-0.5) != -1 {
		t.Fatal("half-away-from-zero rule broken")
	}
	if quantize16(0.4) != 0 || quantize16(-0.4) != 0 {
		t.Fatal("nearest rounding broken")
	}
	// repeated Apply of identical input yields identical output (no drift)
	in := fullScaleTone(500, 0.4)
	m := NewLoudnessMeter(1)
	m.Accumulate(in)
	m.ComputeGain()
	a := m.Apply(in)
	b := m.Apply(in)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("loudness output non-deterministic at %d", i)
		}
	}
}

func TestLoudnessCheckpointResume(t *testing.T) {
	in := fullScaleTone(2000, 0.5)
	m := NewLoudnessMeter(1)
	m.Accumulate(in[:1000])
	snap := m.Snapshot()
	restored := RestoreLoudness(snap)
	m.Accumulate(in[1000:])
	restored.Accumulate(in[1000:])
	if math.Abs(m.LUFS()-restored.LUFS()) > 1e-12 {
		t.Fatalf("resume LUFS diverges: %v vs %v", m.LUFS(), restored.LUFS())
	}
	m.ComputeGain()
	restored.ComputeGain()
	if m.GainDB() != restored.GainDB() {
		t.Fatal("gain diverges after checkpoint restore")
	}
}

func TestTruePeakDBFS(t *testing.T) {
	if p := TruePeakDBFS([]int16{32767, -32767}); p >= 0 || p < -0.001 {
		t.Fatalf("full-scale peak %.3f dBFS", p)
	}
	if !math.IsInf(TruePeakDBFS([]int16{0, 0}), -1) {
		t.Fatal("silence peak should be -Inf")
	}
}
