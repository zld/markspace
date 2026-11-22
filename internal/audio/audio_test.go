package audio

import (
	"math"
	"testing"

	"mediatimeline/internal/clock"
)

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestResampleRateAndPhase(t *testing.T) {
	// 44100 -> 48000 over a 1 kHz sine; compare one-shot vs block streaming.
	const n = 4410 // 100 ms at 44.1k
	one := make([]float64, n)
	for i := range one {
		one[i] = math.Sin(2 * math.Pi * 1000 * float64(i) / 44100)
	}
	r1, _ := NewResampler(44100, 48000, n)
	want := r1.Process(one, 1)
	want = append(want, r1.Flush()...)
	if len(want) != 4800 {
		t.Fatalf("out frames=%d want 4800", len(want))
	}
	// Stream the same input in arbitrary block sizes.
	r2, _ := NewResampler(44100, 48000, n)
	var got []float64
	for off := 0; off < n; {
		end := off + 377
		if end > n {
			end = n
		}
		got = append(got, r2.Process(one[off:end], 1)...)
		off = end
	}
	got = append(got, r2.Flush()...)
	if len(got) != len(want) {
		t.Fatalf("streamed frames=%d want %d", len(got), len(want))
	}
	for i := range want {
		if !approx(got[i], want[i], 1e-12) {
			t.Fatalf("phase drift at %d: %v vs %v", i, got[i], want[i])
		}
	}
	// Round-trip back to 44100 should approximately recover the input length.
	back, _ := NewResampler(48000, 44100, uint32(len(want)))
	if n2 := len(back.Process(want, 1)); n2 < n-1 || n2 > n+1 {
		t.Fatalf("round-trip frames=%d want ~%d", n2, n)
	}
	if _, err := NewResampler(0, 48000, 1); err == nil {
		t.Fatal("zero rate accepted")
	}
}

func TestChannelMappingPolicies(t *testing.T) {
	// Stereo -> 5.1: C/LFE/Ls/Rs missing; L/R map explicitly.
	m, err := BuildMapper(LayoutStereo, Layout51, PolicyZeroFill)
	if err != nil {
		t.Fatal(err)
	}
	in := []float64{0.5, -0.5}
	out := m.Apply(in)
	want := []float64{0.5, -0.5, 0, 0, 0, 0}
	for i := range want {
		if !approx(out[i], want[i], 1e-12) {
			t.Fatalf("zerofill out=%v want %v", out, want)
		}
	}
	if len(m.Errors()) != 1 {
		t.Fatalf("violation not recorded: %+v", m.Errors())
	}
	// Strict policy must reject the same mismatch with a typed error.
	if _, err := BuildMapper(LayoutStereo, Layout51, PolicyError); err == nil {
		t.Fatal("PolicyError did not fire")
	} else if c, ok := err.(*clock.Error); !ok || c.Code != clock.ErrChannelPolicy {
		t.Fatalf("wrong error: %v", err)
	}
	// Downmix of 5.1 -> stereo averages surround channels instead of dropping.
	dm, err := BuildMapper(Layout51, LayoutStereo, PolicyDownmix)
	if err != nil {
		t.Fatal(err)
	}
	surround := []float64{1, 2, 3, 4, 5, 6}
	o := dm.Apply(surround)
	// L=1, R=2 directly; no target is missing here, so no averaging occurs.
	if o[0] != 1 || o[1] != 2 {
		t.Fatalf("direct labels mis-mapped: %v", o)
	}
	// Missing target under downmix averages extra sources.
	mono, _ := BuildMapper(LayoutStereo, LayoutMono, PolicyDownmix)
	mo := mono.Apply([]float64{0.4, 0.8})
	if !approx(mo[0], 0.6, 1e-12) {
		t.Fatalf("downmix avg=%v want 0.6", mo)
	}
}

func TestLoudnessNoClipAndBoundary(t *testing.T) {
	l := NewLoudness(-20, 0.95, 256)
	// Feed a hot signal in blocks that leave a partial final window.
	var total int
	for block := 0; block < 5; block++ {
		in := make([]float64, 300)
		for i := range in {
			in[i] = 1.5 * math.Sin(float64(block*300+i)*0.01)
		}
		out := l.Process(in)
		total += len(out)
		for _, v := range out {
			if math.Abs(v) > 0.95+1e-12 {
				t.Fatalf("ceiling breached: %v", v)
			}
		}
	}
	l.Flush()
	if l.outCount != uint64(total) {
		t.Fatalf("samples lost: out=%d in=%d", l.outCount, total)
	}
	if l.PeakDBFS() > 20*math.Log10(0.95)+1e-9 {
		t.Fatalf("peak %f exceeds ceiling dBFS", l.PeakDBFS())
	}
	if math.IsInf(l.RMSDBFS(), 0) {
		t.Fatal("RMS not computed")
	}
}

func TestLoudnessDeterministic(t *testing.T) {
	build := func() []float64 {
		l := NewLoudness(-18, 0.9, 512)
		var acc []float64
		in := make([]float64, 1000)
		for i := range in {
			in[i] = 0.8 * math.Sin(float64(i)*0.02)
		}
		acc = append(acc, l.Process(in[:400])...)
		acc = append(acc, l.Process(in[400:])...)
		l.Flush()
		return append(acc, l.Gain(), l.PeakDBFS(), l.RMSDBFS())
	}
	a, b := build(), build()
	if len(a) != len(b) {
		t.Fatal("length mismatch")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic output at %d: %v vs %v", i, a[i], b[i])
		}
	}
}
