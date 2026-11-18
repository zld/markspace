package measure

import (
	"math"
	"testing"
)

func feedTP(m *TruePeakMeter, flat [][]float64, blockLen int) {
	n := len(flat[0])
	for start := 0; start < n; start += blockLen {
		end := start + blockLen
		if end > n {
			end = n
		}
		blk := make([][]float64, len(flat))
		for c := range flat {
			blk[c] = flat[c][start:end]
		}
		m.AddBlock(blk)
	}
}

func TestTruePeakSine(t *testing.T) {
	m := NewTruePeakMeter(1)
	feedTP(m, sineFlat(48000, 1, 1, 0.5, 997), 4096)
	dbtp := m.DBTP()
	if dbtp == nil {
		t.Fatal("no true peak")
	}
	want := 20 * math.Log10(0.5)
	if math.Abs(*dbtp-want) > 0.1 {
		t.Errorf("true peak %.3f dBTP, want %.3f", *dbtp, want)
	}
}

func TestTruePeakIntersample(t *testing.T) {
	// Alternating +/-0.5 at Nyquist: sample peak 0.5 but the band-limited
	// reconstruction peaks at 0.5 * 4/pi ~ 0.637 between samples.
	n := 4800
	sig := make([]float64, n)
	for i := range sig {
		if i%2 == 0 {
			sig[i] = 0.5
		} else {
			sig[i] = -0.5
		}
	}
	m := NewTruePeakMeter(1)
	feedTP(m, [][]float64{sig}, 4096)
	peak := m.MaxLinear()[0]
	if peak <= 0.55 {
		t.Errorf("intersample peak not detected: got %.4f, want > 0.55", peak)
	}
	if peak > 0.70 {
		t.Errorf("implausible overshoot: %.4f", peak)
	}
}

func TestTruePeakBlockBoundary(t *testing.T) {
	// An impulse straddling a block boundary must give the same peak as
	// single-block processing.
	n := 4800
	sig := make([]float64, n)
	for i := range sig {
		if i%2 == 0 {
			sig[i] = 0.5
		} else {
			sig[i] = -0.5
		}
	}
	m1 := NewTruePeakMeter(1)
	feedTP(m1, [][]float64{sig}, n)
	m2 := NewTruePeakMeter(1)
	feedTP(m2, [][]float64{sig}, 977) // awkward block size
	p1, p2 := m1.MaxLinear()[0], m2.MaxLinear()[0]
	if math.Abs(p1-p2) > 1e-9 {
		t.Errorf("block boundary changed peak: %.9f vs %.9f", p1, p2)
	}
}

func TestTruePeakNearFullScale(t *testing.T) {
	m := NewTruePeakMeter(2)
	feedTP(m, sineFlat(48000, 2, 1, 0.999, 1234), 4096)
	dbtp := m.DBTP()
	if dbtp == nil || *dbtp < -0.1 || *dbtp > 0.05 {
		t.Errorf("near-full-scale true peak = %v dBTP", dbtp)
	}
}

func TestTruePeakEmptyAndSilence(t *testing.T) {
	m := NewTruePeakMeter(1)
	if m.DBTP() != nil {
		t.Error("empty input must yield nil")
	}
	m2 := NewTruePeakMeter(1)
	feedTP(m2, [][]float64{make([]float64, 4800)}, 4096)
	if m2.DBTP() != nil {
		t.Error("digital silence must yield nil, not a fake number")
	}
}
