package media

import (
	"math"

	"continuity/internal/clock"
)

// BitrateReport is the per-representation bitrate verification result.
type BitrateReport struct {
	TargetBPS    int64
	ActualBPS    int64   // bytes*8 / total duration seconds, integer rounded
	DeviationPct float64 // |actual-target|/target * 100
}

// Bitrate computes the achieved bitrate from total segment bytes and total
// exact duration across a representation. Byte counts include the full
// fMP4 container (moof+mdat), matching how bandwidth is provisioned.
func Bitrate(totalBytes int64, totalDuration clock.Rat) (BitrateReport, error) {
	if totalBytes <= 0 {
		return BitrateReport{}, clock.New(clock.KindInvalidInput, "bitrate",
			"no segment bytes")
	}
	c, err := totalDuration.Cmp(clock.FromInt(0))
	if err != nil {
		return BitrateReport{}, err
	}
	if c <= 0 {
		return BitrateReport{}, clock.New(clock.KindInvalidInput, "bitrate",
			"non-positive total duration")
	}
	// actual = totalBytes*8 / duration. Keep precision through Rat then round.
	bits, err := clock.R(totalBytes*8, 1)
	if err != nil {
		return BitrateReport{}, err
	}
	bpsRat, err := bits.Div(totalDuration)
	if err != nil {
		return BitrateReport{}, err
	}
	bps, err := bpsRat.Round()
	if err != nil {
		return BitrateReport{}, err
	}
	return BitrateReport{ActualBPS: bps}, nil
}

// WithTarget annotates a report with the declared target and deviation.
func (b BitrateReport) WithTarget(target int64) BitrateReport {
	b.TargetBPS = target
	if target > 0 {
		b.DeviationPct = math.Abs(float64(b.ActualBPS-target)) / float64(target) * 100
	}
	return b
}

// GrayFrame is an 8-bit grayscale image with a fixed width/height. The
// acceptance fixtures encode frames directly as raw mdat payloads; no
// general-purpose codec is involved.
type GrayFrame struct {
	Width  int
	Height int
	Pix    []uint8
}

// NewGrayFrame allocates a frame.
func NewGrayFrame(w, h int) *GrayFrame {
	return &GrayFrame{Width: w, Height: h, Pix: make([]byte, w*h)}
}

// PSNR computes the peak signal-to-noise ratio in dB between two equally
// sized 8-bit grayscale frames. Identical frames return +Inf.
func PSNR(a, b *GrayFrame) (float64, error) {
	if a.Width != b.Width || a.Height != b.Height || len(a.Pix) != len(b.Pix) {
		return 0, clock.New(clock.KindInvalidInput, "psnr",
			"frame dimensions differ: %dx%d vs %dx%d",
			a.Width, a.Height, b.Width, b.Height)
	}
	var mse float64
	for i := range a.Pix {
		d := int(a.Pix[i]) - int(b.Pix[i])
		mse += float64(d * d)
	}
	mse /= float64(len(a.Pix))
	if mse == 0 {
		return math.Inf(1), nil
	}
	return 10 * math.Log10(float64(255*255)/mse), nil
}

// FrameFromPayload interprets raw mdat bytes as one grayscale frame.
// w*h must equal len(payload).
func FrameFromPayload(payload []byte, w, h int) (*GrayFrame, error) {
	if w <= 0 || h <= 0 || w*h != len(payload) {
		return nil, clock.New(clock.KindInvalidInput, "psnr",
			"payload %d bytes does not match frame %dx%d", len(payload), w, h)
	}
	f := NewGrayFrame(w, h)
	copy(f.Pix, payload)
	return f, nil
}

// GradientFrame fills a frame with a deterministic horizontal gradient,
// used as the reference for overlap-region PSNR acceptance.
func GradientFrame(w, h int) *GrayFrame {
	f := NewGrayFrame(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f.Pix[y*w+x] = uint8((x * 255) / max1(w-1))
		}
	}
	return f
}

// NoisyFrame returns a copy of src with per-pixel additive noise bounded by
// amp; deterministic given seed, so re-runs reproduce identical PSNR.
func NoisyFrame(src *GrayFrame, amp, seed int) *GrayFrame {
	out := NewGrayFrame(src.Width, src.Height)
	state := uint32(seed)
	next := func() uint32 {
		state = state*1103515245 + 12345
		return state
	}
	for i, v := range src.Pix {
		delta := int(next()%uint32(2*amp+1)) - amp
		nv := int(v) + delta
		if nv < 0 {
			nv = 0
		} else if nv > 255 {
			nv = 255
		}
		out.Pix[i] = uint8(nv)
	}
	return out
}

func max1(a int) int {
	if a < 1 {
		return 1
	}
	return a
}
