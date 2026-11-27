package quality

import (
	"math"
	"testing"
)

func TestSummarize(t *testing.T) {
	cfg := baseConfig()
	s := Summarize(cfg, cand("sel", 1_000_000, 42, 100), ReasonSelected)
	if s.TargetKbps != 800 || math.Abs(s.ActualKbps-800) > 1e-9 {
		t.Fatalf("summary = %+v", s)
	}
	if !s.WithinTolerance || s.Decision != "accept" {
		t.Fatalf("summary = %+v", s)
	}
	outside := Summarize(cfg, cand("off", 2_000_000, 42, 100), ReasonSelectedNoRate)
	if outside.WithinTolerance {
		t.Fatalf("expected outside tolerance: %+v", outside)
	}
}

func TestPSNR(t *testing.T) {
	if PSNR(0, 255) != 100 {
		t.Fatal("perfect match should cap at 100 dB")
	}
	if got := PSNR(1, 255); math.Abs(got-48.13) > 0.01 {
		t.Fatalf("psnr = %g", got)
	}
	if PSNR(1, 0) != 0 {
		t.Fatal("zero peak should be zero")
	}
}
