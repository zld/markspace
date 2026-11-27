package quality

import (
	"math"
	"testing"
)

func solidFrame(w, h int, v byte) *Frame {
	f := NewFrame(w, h)
	for i := range f.Pix {
		f.Pix[i] = v
	}
	return f
}

func TestBitrateAndError(t *testing.T) {
	// 125,000 bytes over 1.0 s = 1,000,000 bps.
	bps, err := Bitrate(125_000, 1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if bps != 1_000_000 {
		t.Fatalf("bitrate: got %d want 1000000", bps)
	}
	// Half a second doubles the rate.
	bps2, _ := Bitrate(125_000, 500_000_000)
	if bps2 != 2_000_000 {
		t.Fatalf("bitrate half-duration: got %d want 2000000", bps2)
	}
	if pct := BitrateErrorPct(1_050_000, 1_000_000); pct < 4.99 || pct > 5.01 {
		t.Fatalf("bitrate error pct: %v", pct)
	}
	if pct := BitrateErrorPct(950_000, 1_000_000); pct < 4.99 || pct > 5.01 {
		t.Fatalf("negative deviation pct: %v", pct)
	}
	if _, err := Bitrate(10, 0); err == nil {
		t.Fatal("zero duration must be rejected")
	}
}

func TestPSNRIdenticalAndDistorted(t *testing.T) {
	a := solidFrame(16, 16, 100)
	psnr, err := PSNR(a, solidFrame(16, 16, 100))
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsInf(psnr, 1) {
		t.Fatalf("identical frames must be +Inf PSNR, got %v", psnr)
	}
	// Uniform +1 distortion: MSE = 1 -> PSNR = 20*log10(255).
	b := solidFrame(16, 16, 101)
	psnr2, err := PSNR(a, b)
	if err != nil {
		t.Fatal(err)
	}
	want := 20 * math.Log10(255)
	if math.Abs(psnr2-want) > 1e-9 {
		t.Fatalf("uniform +1 PSNR: got %v want %v", psnr2, want)
	}
	if psnr2 < 30 {
		t.Fatal("small distortion should exceed the 30 dB acceptance bar")
	}
}

func TestSSIMIdenticalAndDegraded(t *testing.T) {
	a := NewFrame(16, 16)
	for i := range a.Pix {
		a.Pix[i] = byte(i % 200)
	}
	same, err := SSIM(a, a)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(same-1.0) > 1e-12 {
		t.Fatalf("identical frame SSIM must be 1, got %v", same)
	}
	// Heavy additive noise should drive SSIM well below 0.95.
	noisy := NewFrame(16, 16)
	for i := range a.Pix {
		v := int(a.Pix[i]) + ((i * 37) % 120) - 60
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		noisy.Pix[i] = byte(v)
	}
	s, err := SSIM(a, noisy)
	if err != nil {
		t.Fatal(err)
	}
	if s >= 0.95 {
		t.Fatalf("noisy SSIM should be below 0.95, got %v", s)
	}
}

func TestSSIMNonMultipleDimensions(t *testing.T) {
	// 13x11 is not a multiple of the 8x8 window; must still cover all pixels.
	a := NewFrame(13, 11)
	b := NewFrame(13, 11)
	for i := range a.Pix {
		a.Pix[i] = byte(i)
		b.Pix[i] = byte(i)
	}
	s, err := SSIM(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(s-1.0) > 1e-12 {
		t.Fatalf("odd-size identical SSIM: got %v", s)
	}
}

func TestSizeMismatch(t *testing.T) {
	if _, err := PSNR(NewFrame(8, 8), NewFrame(8, 4)); err == nil {
		t.Fatal("size mismatch must error")
	}
	if _, err := SSIM(NewFrame(8, 8), NewFrame(4, 8)); err == nil {
		t.Fatal("size mismatch must error")
	}
}

func TestAggregateDeterministic(t *testing.T) {
	v := Aggregate([]float64{1.0, 2.0, 3.0, 4.0})
	if v != 2.5 {
		t.Fatalf("aggregate: got %v want 2.5", v)
	}
	if Aggregate(nil) != 0 {
		t.Fatal("empty aggregate must be zero")
	}
}
