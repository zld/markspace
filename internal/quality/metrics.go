// Package quality computes deterministic encoding-quality metrics: achieved
// bitrate, PSNR and SSIM. All inputs are integer pixel frames; no floating
// point is used for time (durations arrive as integer nanoseconds), and the
// metrics themselves are pure functions of the frame bytes so repeated runs
// report identical numbers.
package quality

import (
	"math"

	"mediad/internal/model"
)

// Frame is an 8-bit grayscale or interleaved planar-agnostic pixel buffer.
// Stride is the number of bytes per row; Pix holds Stride*Height bytes.
type Frame struct {
	Width  int
	Height int
	Stride int
	Pix    []byte
}

// NewFrame allocates a frame with a tightly packed stride.
func NewFrame(w, h int) *Frame {
	return &Frame{Width: w, Height: h, Stride: w, Pix: make([]byte, w*h)}
}

// Bitrate computes the achieved bitrate in bits per second from byte length
// and an integer-nanosecond duration. The division is rounded once.
func Bitrate(byteLen int64, durationNS int64) (int64, error) {
	if durationNS <= 0 {
		return 0, model.New(model.ErrZeroTimebase, "", -1, "bitrate needs a positive duration, got %d ns", durationNS)
	}
	bits, err := model.CheckedMul(byteLen, 8)
	if err != nil {
		return 0, err
	}
	// bits * 1e9 / ns, guarding the multiplication.
	scaled, err := model.CheckedMul(bits, model.NanoPerSec)
	if err != nil {
		return 0, err
	}
	return model.RoundDiv(scaled, durationNS)
}

// BitrateErrorPct returns the absolute percentage error between achieved and
// target bitrate, rounded to four decimal places as a float (reporting only,
// never accumulated). Returns 100*|actual-target|/target.
func BitrateErrorPct(actual, target int64) float64 {
	if target == 0 {
		return 0
	}
	diff := actual - target
	if diff < 0 {
		diff = -diff
	}
	pct := float64(diff) * 100.0 / float64(target)
	return math.Round(pct*10000) / 10000
}

// PSNR computes the peak signal-to-noise ratio in dB between two equally sized
// frames. Identical frames return +Inf (reported as a sentinel by callers).
func PSNR(a, b *Frame) (float64, error) {
	if err := checkSameSize(a, b); err != nil {
		return 0, err
	}
	var se uint64
	n := 0
	for y := 0; y < a.Height; y++ {
		rowA := a.Pix[y*a.Stride : y*a.Stride+a.Width]
		rowB := b.Pix[y*b.Stride : y*b.Stride+b.Width]
		for x := 0; x < a.Width; x++ {
			d := int64(rowA[x]) - int64(rowB[x])
			se += uint64(d * d)
			n++
		}
	}
	if se == 0 {
		return math.Inf(1), nil
	}
	mse := float64(se) / float64(n)
	return 10.0 * math.Log10((255.0*255.0)/mse), nil
}

// SSIM computes the mean structural similarity over non-overlapping 8x8
// windows using the standard constants for 8-bit dynamic range
// (K1=0.01, K2=0.03, L=255). Windows that do not fit are folded into the last
// block, so the whole frame is covered exactly once.
func SSIM(a, b *Frame) (float64, error) {
	if err := checkSameSize(a, b); err != nil {
		return 0, err
	}
	const (
		k1 = 0.01
		k2 = 0.03
		l  = 255.0
		bs = 8
	)
	c1 := (k1 * l) * (k1 * l)
	c2 := (k2 * l) * (k2 * l)

	var sum float64
	var blocks int
	for y0 := 0; y0 < a.Height; y0 += bs {
		y1 := y0 + bs
		if y1 > a.Height {
			y1 = a.Height
		}
		for x0 := 0; x0 < a.Width; x0 += bs {
			x1 := x0 + bs
			if x1 > a.Width {
				x1 = a.Width
			}
			var n, sa, sb float64
			var sa2, sb2, sab float64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					va := float64(a.Pix[y*a.Stride+x])
					vb := float64(b.Pix[y*b.Stride+x])
					n++
					sa += va
					sb += vb
					sa2 += va * va
					sb2 += vb * vb
					sab += va * vb
				}
			}
			muA := sa / n
			muB := sb / n
			varA := sa2/n - muA*muA
			varB := sb2/n - muB*muB
			covAB := sab/n - muA*muB
			num := (2*muA*muB + c1) * (2*covAB + c2)
			den := (muA*muA + muB*muB + c1) * (varA + varB + c2)
			sum += num / den
			blocks++
		}
	}
	if blocks == 0 {
		return 0, model.New(model.ErrStageState, "", -1, "SSIM over an empty frame set")
	}
	return sum / float64(blocks), nil
}

// Aggregate averages per-frame metric values, rounding to a stable precision.
func Aggregate(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var s float64
	for _, v := range values {
		s += v
	}
	return math.Round((s/float64(len(values)))*1_000_000) / 1_000_000
}

// PSNRInfSentinel replaces +Inf (lossless) in reports with this finite value,
// keeping the terminal/JSON output well-defined.
const PSNRInfSentinel = 99.0

func checkSameSize(a, b *Frame) error {
	if a.Width != b.Width || a.Height != b.Height {
		return model.New(model.ErrStageState, "", -1,
			"frame size mismatch: %dx%d vs %dx%d", a.Width, a.Height, b.Width, b.Height)
	}
	return nil
}
