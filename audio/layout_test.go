package audio

import "testing"

func TestMonoToStereoZeroFill(t *testing.T) {
	in := []int16{1000, -2000, 3000}
	res, err := Normalize(in, 3, Mono, Stereo, FillZero)
	if err != nil {
		t.Fatal(err)
	}
	// Mono is ChC which has no L/R position: both target channels are zero-filled.
	want := []int16{0, 0, 0, 0, 0, 0}
	if len(res.Out) != len(want) || res.ZeroFilled != 6 {
		t.Fatalf("out=%v zeroFilled=%d", res.Out, res.ZeroFilled)
	}
	for i := range want {
		if res.Out[i] != want[i] {
			t.Fatalf("sample %d = %d want %d", i, res.Out[i], want[i])
		}
	}
}

func TestMonoToStereoDownmix(t *testing.T) {
	in := []int16{1000, -2000}
	res, err := Normalize(in, 2, Mono, Stereo, Downmix)
	if err != nil {
		t.Fatal(err)
	}
	// Centre-only source folds to L/R as C/2 (documented fold-down weight).
	want := []int16{500, 500, -1000, -1000}
	for i := range want {
		if res.Out[i] != want[i] {
			t.Fatalf("sample %d = %d want %d (full %v)", i, res.Out[i], want[i], res.Out)
		}
	}
	if res.Downmixed != 4 {
		t.Fatalf("downmixed=%d", res.Downmixed)
	}
}

func TestStereoToMonoCentreFold(t *testing.T) {
	in := []int16{1000, 3000, -1000, -3000}
	res, err := Normalize(in, 2, Stereo, Mono, Downmix)
	if err != nil {
		t.Fatal(err)
	}
	// C = (L+R)/2 => 2000, -2000
	if res.Out[0] != 2000 || res.Out[1] != -2000 {
		t.Fatalf("mono fold = %v", res.Out)
	}
}

func TestStereoTo51ZeroFillMissing(t *testing.T) {
	in := []int16{100, 200} // one stereo frame L,R
	res, err := Normalize(in, 1, Stereo, Surround51, FillZero)
	if err != nil {
		t.Fatal(err)
	}
	// order L,R,C,LFE,Ls,Rs: L and R kept, other four zero-filled
	want := []int16{100, 200, 0, 0, 0, 0}
	if len(res.Out) != 6 || res.ZeroFilled != 4 {
		t.Fatalf("out=%v zeroFilled=%d", res.Out, res.ZeroFilled)
	}
	for i := range want {
		if res.Out[i] != want[i] {
			t.Fatalf("channel %d = %d want %d", i, res.Out[i], want[i])
		}
	}
}

func Test51ToStereoPassthroughAndLFEClipped(t *testing.T) {
	// L,R,C,LFE,Ls,Rs
	in := []int16{1000, 2000, 400, 30000, 200, 600}
	res, err := Normalize(in, 1, Surround51, Stereo, Downmix)
	if err != nil {
		t.Fatal(err)
	}
	// L and R exist in the source, so they pass through unchanged; the
	// surrounds and LFE cannot be placed in the stereo target.
	if res.Out[0] != 1000 || res.Out[1] != 2000 {
		t.Fatalf("passthrough = %v", res.Out)
	}
	if res.Dropped == 0 {
		t.Fatal("non-placeable channels (LFE/Ls/Rs/C) should be reported dropped")
	}
}

func TestFoldDownWhenStereoSidesAbsent(t *testing.T) {
	// Source carries C, Ls, Rs but no L/R: stereo targets must be synthesised.
	src := Layout{"c+ls+rs", []Channel{ChC, ChLs, ChRs}}
	in := []int16{400, 200, 600}
	res, err := Normalize(in, 1, src, Stereo, Downmix)
	if err != nil {
		t.Fatal(err)
	}
	// L' = C/2 + Ls/2 = 200 + 100 = 300
	// R' = C/2 + Rs/2 = 200 + 300 = 500
	if res.Out[0] != 300 || res.Out[1] != 500 {
		t.Fatalf("fold = %v", res.Out)
	}
	if res.Downmixed != 2 {
		t.Fatalf("downmixed=%d", res.Downmixed)
	}
}

func TestSaturate16ClampsBothRails(t *testing.T) {
	// Fold-down weights halve every contribution, so the saturate guard is a
	// defensive rail; validate it directly at both extremes.
	if v, sat := saturate16(40000); !sat || v != 32767 {
		t.Fatalf("positive clamp broken: v=%d sat=%v", v, sat)
	}
	if v, sat := saturate16(-40000); !sat || v != -32768 {
		t.Fatalf("negative clamp broken: v=%d sat=%v", v, sat)
	}
	if v, sat := saturate16(1234); sat || v != 1234 {
		t.Fatalf("in-range value altered: v=%d sat=%v", v, sat)
	}
}

func TestNormalizeRejectsBadFrameCount(t *testing.T) {
	if _, err := Normalize([]int16{1, 2, 3}, 2, Stereo, Mono, FillZero); err == nil {
		t.Fatal("length/frame mismatch accepted")
	}
	if _, err := Normalize(nil, -1, Stereo, Mono, FillZero); err == nil {
		t.Fatal("negative frame count accepted")
	}
}

func TestLayoutDeterministic(t *testing.T) {
	in := []int16{1000, 2000, 300, 0, 100, 500}
	r1, _ := Normalize(in, 1, Surround51, Stereo, Downmix)
	r2, _ := Normalize(in, 1, Surround51, Stereo, Downmix)
	for i := range r1.Out {
		if r1.Out[i] != r2.Out[i] {
			t.Fatal("non-deterministic layout output")
		}
	}
}
