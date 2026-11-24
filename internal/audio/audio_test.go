package audio

import (
	"math"
	"testing"
)

func TestSilenceHasFiniteLoudness(t *testing.T) {
	b := MustNew(48000, LayoutStereo, 1024)
	rep, err := NormalizeLoudness(b, -16, -1.0)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(rep.IntegratedLUFS) || math.IsNaN(rep.PeakDBFS) {
		t.Fatalf("NaN in silence report: %+v", rep)
	}
	if !rep.Silent || rep.IntegratedLUFS != SilenceFloorDB {
		t.Fatalf("unexpected silence report: %+v", rep)
	}
	for c := range b.Planar {
		for _, v := range b.Planar[c] {
			if math.IsNaN(v) {
				t.Fatal("NaN sample after normalizing silence")
			}
		}
	}
}

func TestNormalizeTargetAndPeakCeiling(t *testing.T) {
	b := MustNew(48000, LayoutStereo, 4800)
	for i := range b.Planar[0] {
		v := 0.05 * math.Sin(2*math.Pi*440*float64(i)/48000)
		b.Planar[0][i] = v
		b.Planar[1][i] = v
	}
	const ceiling = -6.0
	rep, err := NormalizeLoudness(b, -16, ceiling)
	if err != nil {
		t.Fatal(err)
	}
	if d := math.Abs(rep.IntegratedLUFS + 16); d > 0.05 {
		t.Fatalf("LUFS %.4f not normalized to -16", rep.IntegratedLUFS)
	}
	if rep.PeakDBFS > ceiling+1e-9 {
		t.Fatalf("peak %.4f exceeds ceiling %.1f", rep.PeakDBFS, ceiling)
	}
}

func TestLayoutMonoToStereoAndBack(t *testing.T) {
	m := MustNew(48000, LayoutMono, 4)
	for i := 0; i < 4; i++ {
		m.Planar[0][i] = 0.25
	}
	st, err := ConvertLayout(m, LayoutStereo)
	if err != nil {
		t.Fatal(err)
	}
	if st.Layout.Channels != 2 || st.Planar[0][0] != .25 || st.Planar[1][0] != .25 {
		t.Fatalf("bad mono->stereo: %+v", st)
	}
	back, err := ConvertLayout(st, LayoutMono)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(back.Planar[0][0]-.25) > 1e-12 {
		t.Fatalf("stereo->mono changed dual-mono sample: %v", back.Planar[0][0])
	}
}

func TestResamplePreservesDurationAndStatelessRegions(t *testing.T) {
	in := MustNew(44100, LayoutStereo, 44100)
	for i := range in.Planar[0] {
		v := math.Sin(2 * math.Pi * 1000 * float64(i) / 44100)
		in.Planar[0][i] = v
		in.Planar[1][i] = v
	}
	out, err := Resample(in, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Frames(); got < 47999 || got > 48001 {
		t.Fatalf("resampled frames %d not ~48000", got)
	}
	first, err := ResampleRegion(in, 0, 100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	// The second region starts at the exact nanosecond representation of
	// output sample 100. Rounding must stay bounded rather than accumulate.
	const startNs = int64(100) * 1_000_000_000 / 48000
	second, err := ResampleRegion(in, startNs, 100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	whole, err := ResampleRegion(in, 0, 200, 48000)
	if err != nil {
		t.Fatal(err)
	}
	maxDiff := 0.0
	for i := 0; i < 100; i++ {
		// Nanosecond rounding can shift the source position by less than
		// one interpolation quantum; compare against the contiguous render.
		if d := math.Abs(second.Planar[0][i] - whole.Planar[0][100+i]); d > maxDiff {
			maxDiff = d
		}
	}
	if maxDiff > 0.02 {
		t.Fatalf("chunk boundary resampling mismatch %f", maxDiff)
	}
	if first.Frames() != 100 || second.Frames() != 100 {
		t.Fatal("region frame count changed")
	}
}
