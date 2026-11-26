// Package render performs deterministic off-screen reference rendering and metrics.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
)

// RGBMean contains channel means on the conventional 0..255 scale.
type RGBMean struct {
	R float64 `json:"r"`
	G float64 `json:"g"`
	B float64 `json:"b"`
}

// ComputeRGBMean computes non-alpha-weighted RGB means from tightly packed RGBA8.
func ComputeRGBMean(rgba []byte) (RGBMean, error) {
	if len(rgba) == 0 || len(rgba)%4 != 0 {
		return RGBMean{}, fmt.Errorf("RGBA buffer length %d is not a positive multiple of 4", len(rgba))
	}
	var sr, sg, sb uint64
	pixels := uint64(len(rgba) / 4)
	for i := 0; i < len(rgba); i += 4 {
		sr += uint64(rgba[i])
		sg += uint64(rgba[i+1])
		sb += uint64(rgba[i+2])
	}
	return RGBMean{
		R: float64(sr) / float64(pixels),
		G: float64(sg) / float64(pixels),
		B: float64(sb) / float64(pixels),
	}, nil
}

// PeakDBFS returns the sample peak for signed 16-bit PCM.
func PeakDBFS(samples []int16) float64 {
	var peak int64
	for _, v := range samples {
		a := int64(v)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	if peak == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(float64(peak)/32768.0)
}

// HashBytes returns a lowercase 64-character SHA-256 hex digest.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashFile hashes an open seekable/readable file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyRGBA recomputes RGB means and compares them within tolerance.
func VerifyRGBA(path string, want RGBMean, tolerance float64) (RGBMean, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return RGBMean{}, err
	}
	got, err := ComputeRGBMean(b)
	if err != nil {
		return RGBMean{}, err
	}
	if math.Abs(got.R-want.R) > tolerance || math.Abs(got.G-want.G) > tolerance || math.Abs(got.B-want.B) > tolerance {
		return got, fmt.Errorf("RGB mean mismatch: got %+v want %+v tol %v", got, want, tolerance)
	}
	return got, nil
}
