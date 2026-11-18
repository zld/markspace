package measure

import (
	"math"
	"testing"
)

// sineFlat renders a sine into deinterleaved per-channel slices.
func sineFlat(rate, channels int, seconds, amp, freq float64) [][]float64 {
	n := int(seconds * float64(rate))
	out := make([][]float64, channels)
	for c := range out {
		out[c] = make([]float64, n)
		for i := 0; i < n; i++ {
			out[c][i] = amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate))
		}
	}
	return out
}

// feed pushes flat samples through m in blockLen-frame chunks.
func feed(m *LoudnessMeter, flat [][]float64, blockLen int) {
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

func newStereoMeter(t *testing.T) *LoudnessMeter {
	t.Helper()
	w, _ := ChannelWeights(2)
	m, err := NewLoudnessMeter(48000, w)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestIntegratedSine(t *testing.T) {
	m := newStereoMeter(t)
	// Stereo sine, amp 0.1: energy = 2 * 0.1^2/2 = 0.01 -> -20.691 LUFS
	// plus a small K-weight gain near 1 kHz.
	feed(m, sineFlat(48000, 2, 3, 0.1, 997), 4096)
	res := m.Result()
	if res.Status != StatusOK || res.Integrated == nil {
		t.Fatalf("status %s, integrated %v", res.Status, res.Integrated)
	}
	if *res.Integrated < -21.0 || *res.Integrated > -19.4 {
		t.Errorf("integrated = %.2f LUFS, want near -20.1", *res.Integrated)
	}
	if res.ShortTermMax == nil || *res.ShortTermMax < *res.Integrated-0.5 {
		t.Errorf("short-term max %v inconsistent with integrated %.2f",
			res.ShortTermMax, *res.Integrated)
	}
	if res.BlocksTotal == 0 || res.BlocksAbs != res.BlocksTotal || res.BlocksRel != res.BlocksTotal {
		t.Errorf("block counts wrong: %+v", res)
	}
}

func TestSilenceBelowGate(t *testing.T) {
	m := newStereoMeter(t)
	feed(m, sineFlat(48000, 2, 2, 0.0, 997), 4096)
	res := m.Result()
	if res.Status != StatusBelowGate {
		t.Errorf("status = %s, want %s", res.Status, StatusBelowGate)
	}
	if res.Integrated != nil || res.ShortTermMax != nil {
		t.Errorf("silence must not produce values: %+v", res)
	}
}

func TestEmptyInput(t *testing.T) {
	m := newStereoMeter(t)
	res := m.Result()
	if res.Status != StatusEmpty || res.Integrated != nil || res.ShortTermMax != nil {
		t.Errorf("empty input: %+v", res)
	}
}

func TestShortMaterialPartialWindow(t *testing.T) {
	m := newStereoMeter(t)
	feed(m, sineFlat(48000, 2, 0.1, 0.1, 997), 4096) // 100 ms < 400 ms window
	res := m.Result()
	if res.Status != StatusOK || res.Integrated == nil {
		t.Fatalf("short material: %+v", res)
	}
	if res.BlocksTotal != 1 {
		t.Errorf("blocks = %d, want 1 partial block", res.BlocksTotal)
	}
}

func TestRelativeGateExcludesQuietBlocks(t *testing.T) {
	m := newStereoMeter(t)
	loud := sineFlat(48000, 2, 1, 0.3, 997)
	quiet := sineFlat(48000, 2, 3, 0.001, 997)
	for c := range loud {
		loud[c] = append(loud[c], quiet[c]...)
	}
	feed(m, loud, 4096)
	res := m.Result()
	// Reference: loud-only measurement.
	ref := newStereoMeter(t)
	feed(ref, sineFlat(48000, 2, 1, 0.3, 997), 4096)
	want := ref.Result()
	if res.Integrated == nil || want.Integrated == nil {
		t.Fatal("missing integrated values")
	}
	// Blocks straddling the loud/quiet boundary legitimately pass the
	// relative gate, so allow for their pull on the mean.
	if d := *res.Integrated - *want.Integrated; math.Abs(d) > 1.0 {
		t.Errorf("gated integrated %.2f deviates %.2f LU from loud-only %.2f",
			*res.Integrated, d, *want.Integrated)
	}
	if res.BlocksRel >= res.BlocksTotal {
		t.Errorf("relative gate did not exclude quiet blocks: %+v", res)
	}
}

func TestLFEExcluded(t *testing.T) {
	w, _ := ChannelWeights(6)
	m, err := NewLoudnessMeter(48000, w)
	if err != nil {
		t.Fatal(err)
	}
	// Signal only on the LFE channel (index 3, weight 0).
	flat := make([][]float64, 6)
	flat[3] = sineFlat(48000, 1, 2, 0.5, 55)[0]
	for c := range flat {
		if flat[c] == nil {
			flat[c] = make([]float64, len(flat[3]))
		}
	}
	feed(m, flat, 4096)
	res := m.Result()
	if res.Status != StatusBelowGate || res.Integrated != nil {
		t.Errorf("LFE-only signal must not register: %+v", res)
	}
}

func TestBlockBoundariesConsistent(t *testing.T) {
	flat := sineFlat(48000, 2, 2, 0.1, 997)
	m1 := newStereoMeter(t)
	feed(m1, flat, 48000) // one block
	m2 := newStereoMeter(t)
	feed(m2, flat, 977) // awkward block size
	r1, r2 := m1.Result(), m2.Result()
	if r1.Integrated == nil || r2.Integrated == nil {
		t.Fatal("missing integrated")
	}
	if d := *r1.Integrated - *r2.Integrated; math.Abs(d) > 0.05 {
		t.Errorf("block size changed result: %.3f vs %.3f", *r1.Integrated, *r2.Integrated)
	}
}
