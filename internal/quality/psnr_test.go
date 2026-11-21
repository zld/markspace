package quality

import (
	"strings"
	"testing"

	"bitrack/internal/model"
)

func makeFrames(n, size int, fill byte) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		f := make([]byte, size)
		for j := range f {
			f[j] = fill
		}
		out[i] = f
	}
	return out
}

func TestCompareFramesIdentical(t *testing.T) {
	w, h := 16, 16
	size := w * h * 3 / 2
	a := makeFrames(3, size, 100)
	b := makeFrames(3, size, 100)
	res, err := CompareFrames(a, b, w, h)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range res.FramePSNR {
		if v != identicalPSNR {
			t.Errorf("identical frames PSNR = %.1f, want %.1f", v, identicalPSNR)
		}
	}
}

func TestCompareFramesMismatchHardFails(t *testing.T) {
	w, h := 16, 16
	size := w * h * 3 / 2
	_, err := CompareFrames(makeFrames(2, size, 1), makeFrames(3, size, 1), w, h)
	if err == nil || !strings.Contains(err.Error(), "frame count mismatch") {
		t.Fatalf("expected frame count mismatch error, got %v", err)
	}
	bad := makeFrames(1, size-1, 1)
	_, err = CompareFrames(makeFrames(1, size, 1), bad, w, h)
	if err == nil || !strings.Contains(err.Error(), "dimension mismatch") {
		t.Fatalf("expected dimension mismatch error, got %v", err)
	}
}

func TestCompareFramesKnownDistortion(t *testing.T) {
	w, h := 16, 16
	size := w * h * 3 / 2
	a := makeFrames(1, size, 100)
	distorted := makeFrames(1, size, 100)
	// Apply a uniform +10 luma shift: MSE = 100, PSNR is exactly computable.
	for i := 0; i < w*h; i++ {
		distorted[0][i] = 110
	}
	res, err := CompareFrames(a, distorted, w, h)
	if err != nil {
		t.Fatal(err)
	}
	// MSE = 100 over the luma plane: PSNR = 10*log10(255^2/100) = 28.1308 dB.
	want := 10 * 2.81308036086791 // 10*log10(650.25)
	if diff := res.MeanDB - want; diff > 0.01 || diff < -0.01 {
		t.Errorf("PSNR = %.4f, want %.4f", res.MeanDB, want)
	}
}

func TestAggregateRegressions(t *testing.T) {
	segs := []*model.Segment{
		{Index: 0, PSNRDB: 30, BaselinePSNR: 30.5}, // drop 0.5
		{Index: 1, PSNRDB: 28, BaselinePSNR: 30},   // drop 2.0
		{Index: 2, PSNRDB: 35, BaselinePSNR: 34},   // improvement
	}
	_, _, reg := Aggregate(segs, 1.0)
	if reg != 1 {
		t.Errorf("regression count = %d, want 1", reg)
	}
	_, std, _ := Aggregate(segs, 1.0)
	if std <= 0 {
		t.Errorf("stddev should be positive, got %f", std)
	}
}
