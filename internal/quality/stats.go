package quality

import (
	"math"

	"gonum.org/v1/gonum/floats"
	"gonum.org/v1/gonum/stat"
)

// SaturationDB replaces +Inf PSNR (bit-identical frames) so means stay
// finite. The value is recorded in the report.
const SaturationDB = 99.0

// Summary aggregates per-frame PSNR values.
type Summary struct {
	MeanDB    float64 `json:"mean_db"`
	MinDB     float64 `json:"min_db"`
	Frames    int     `json:"frames"`
	InfFrames int     `json:"inf_frames"`
}

// Summarize computes mean and minimum over per-frame PSNR. Infinite values
// (identical frames) are counted separately and saturated at SaturationDB.
func Summarize(psnr []float64) Summary {
	s := Summary{Frames: len(psnr)}
	if len(psnr) == 0 {
		return s
	}
	vals := make([]float64, len(psnr))
	for i, v := range psnr {
		if math.IsInf(v, 1) {
			s.InfFrames++
			v = SaturationDB
		}
		vals[i] = v
	}
	s.MeanDB = stat.Mean(vals, nil)
	s.MinDB = floats.Min(vals)
	return s
}
