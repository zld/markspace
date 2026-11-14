package quality

import (
	"math"
	"strings"
	"testing"
)

const sample = `n:1 mse_avg:120.50 mse_y:130.0 mse_u:100.0 mse_v:95.0 psnr_avg:27.32 psnr_y:26.99 psnr_u:28.13 psnr_v:28.36
n:2 mse_avg:0.00 mse_y:0.00 mse_u:0.00 mse_v:0.00 psnr_avg:inf psnr_y:inf psnr_u:inf psnr_v:inf
n:3 mse_avg:80.25 mse_y:90.0 mse_u:60.0 mse_v:70.0 psnr_avg:29.09 psnr_y:28.59 psnr_u:30.35 psnr_v:29.68
`

func TestParsePSNRStats(t *testing.T) {
	vals, err := ParsePSNRStats(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 3 {
		t.Fatalf("got %d frames, want 3", len(vals))
	}
	if vals[0] != 27.32 || vals[2] != 29.09 {
		t.Fatalf("values = %v", vals)
	}
	if !math.IsInf(vals[1], 1) {
		t.Fatalf("frame 2 should be +Inf, got %v", vals[1])
	}
}

func TestParsePSNRStatsMalformed(t *testing.T) {
	if _, err := ParsePSNRStats(strings.NewReader("garbage line\n")); err == nil {
		t.Fatal("expected error for malformed line")
	}
	if _, err := ParsePSNRStats(strings.NewReader("n:1 mse_avg:1.0\n")); err == nil {
		t.Fatal("expected error for missing psnr_avg")
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]float64{40, 50, math.Inf(1)})
	if s.Frames != 3 || s.InfFrames != 1 {
		t.Fatalf("summary = %+v", s)
	}
	wantMean := (40 + 50 + SaturationDB) / 3
	if math.Abs(s.MeanDB-wantMean) > 1e-9 {
		t.Fatalf("mean = %v, want %v", s.MeanDB, wantMean)
	}
	if s.MinDB != 40 {
		t.Fatalf("min = %v", s.MinDB)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	s := Summarize(nil)
	if s.Frames != 0 || s.MeanDB != 0 {
		t.Fatalf("summary = %+v", s)
	}
}
