// Package quality computes frame-level PSNR between source and encoded
// clips and summarizes the results. Black and identical frames have
// explicit definitions so zero MSE can never produce NaN or Inf that
// contaminates aggregate statistics.
package quality

import (
	"errors"
	"fmt"
	"math"

	"transcode/internal/media"
)

// maxPixelValue is the 8-bit grayscale dynamic range used as PSNR MAX_I.
const maxPixelValue = 255.0

// FramePSNR holds one frame's measurement with edge-case annotations.
type FramePSNR struct {
	Index     int
	PSNRDB    float64
	MSE       float64
	Identical bool // source and encoded frame are pixel-for-pixel equal
	BlackPair bool // both frames are entirely zero
}

var (
	// ErrFrameCountMismatch means source and encoded clip lengths differ,
	// which indicates a truncated or corrupt encoder output.
	ErrFrameCountMismatch = errors.New("frame count mismatch")
	// ErrPlaneMismatch means frame dimensions do not agree.
	ErrPlaneMismatch = errors.New("frame plane mismatch")
)

func isBlack(frame []byte) bool {
	for _, b := range frame {
		if b != 0 {
			return false
		}
	}
	return true
}

// Frame computes one frame's PSNR. Definitions:
//
//   - Identical frames (MSE == 0) where at least one side is non-black:
//     PSNR is reported as +Inf and flagged Identical; aggregates skip these.
//   - Two all-zero frames: MSE 0/0 is undefined; PSNR is reported as NaN
//     and flagged BlackPair; aggregates skip these too.
//   - Every other frame: 10 * log10(MAX_I^2 / MSE), a finite value.
func Frame(index int, src, enc []byte) (FramePSNR, error) {
	if len(src) != len(enc) {
		return FramePSNR{}, fmt.Errorf("%w: frame %d: %d vs %d bytes",
			ErrPlaneMismatch, index, len(src), len(enc))
	}
	sum := 0.0
	identical := true
	for i := range src {
		d := float64(src[i]) - float64(enc[i])
		if d != 0 {
			identical = false
		}
		sum += d * d
	}
	mse := sum / float64(len(src))
	res := FramePSNR{Index: index, MSE: mse, Identical: identical,
		BlackPair: isBlack(src) && isBlack(enc)}
	switch {
	case identical && res.BlackPair:
		res.PSNRDB = math.NaN()
	case identical:
		res.PSNRDB = math.Inf(1)
	default:
		res.PSNRDB = 10 * math.Log10(maxPixelValue*maxPixelValue/mse)
	}
	return res, nil
}

// Clip measures every matching frame pair between two clips.
func Clip(src, enc *media.Video) ([]FramePSNR, error) {
	if src.Width != enc.Width || src.Height != enc.Height {
		return nil, fmt.Errorf("%w: %dx%d vs %dx%d", ErrPlaneMismatch,
			src.Width, src.Height, enc.Width, enc.Height)
	}
	if len(src.Frames) != len(enc.Frames) {
		return nil, fmt.Errorf("%w: source %d frames vs encoded %d",
			ErrFrameCountMismatch, len(src.Frames), len(enc.Frames))
	}
	out := make([]FramePSNR, len(src.Frames))
	for i := range src.Frames {
		f, err := Frame(i, src.Frames[i], enc.Frames[i])
		if err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}

// SegmentSummary aggregates finite PSNR values only. Identical and black
// pairs are counted separately and never feed mean/min/max; if every frame
// is an edge case the mean is reported as NaN rather than a fake number.
type SegmentSummary struct {
	FrameCount     int
	FiniteCount    int
	IdenticalPairs int
	BlackPairs     int
	MeanDB         float64
	MinDB          float64
	MaxDB          float64
}

// Summarize reduces frame measurements to one segment summary.
func Summarize(frames []FramePSNR) SegmentSummary {
	s := SegmentSummary{FrameCount: len(frames),
		MeanDB: math.NaN(), MinDB: math.NaN(), MaxDB: math.NaN()}
	sum := 0.0
	first := true
	for _, f := range frames {
		switch {
		case f.BlackPair:
			s.BlackPairs++
			continue
		case f.Identical:
			s.IdenticalPairs++
			continue
		}
		if math.IsNaN(f.PSNRDB) || math.IsInf(f.PSNRDB, 0) {
			continue // defensive: no non-finite value may enter the aggregate
		}
		s.FiniteCount++
		sum += f.PSNRDB
		if first || f.PSNRDB < s.MinDB {
			s.MinDB = f.PSNRDB
		}
		if first || f.PSNRDB > s.MaxDB {
			s.MaxDB = f.PSNRDB
		}
		first = false
	}
	if s.FiniteCount > 0 {
		s.MeanDB = sum / float64(s.FiniteCount)
	}
	return s
}
