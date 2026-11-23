package metrics

import (
	"mediainspect/internal/stream"
)

// QualityStats summarizes deterministic per-frame quality estimates.
type QualityStats struct {
	PSNRMeanDB   float64 `json:"quality_psnr_mean_db"`
	PSNRMinDB    float64 `json:"quality_psnr_min_db"`
	Frames       int     `json:"quality_frames"`
	CorruptFrames int    `json:"quality_corrupt_frames"`
}

// framePSNR is a deterministic synthetic quality estimate for one frame:
// healthy frames sit near 48 dB with a small sequence-derived variation,
// damaged frames drop to 15 dB.
func framePSNR(o stream.OutputFrame) float64 {
	if o.Damaged {
		return 15.0
	}
	return 48.0 - float64(o.Frame.Seq%8)*0.05
}

// Quality aggregates per-frame quality over the executed output.
func Quality(outputs []stream.OutputFrame) QualityStats {
	st := QualityStats{Frames: len(outputs)}
	if len(outputs) == 0 {
		return st
	}
	st.PSNRMinDB = 1e9
	var sum float64
	for _, o := range outputs {
		p := framePSNR(o)
		if o.Damaged {
			st.CorruptFrames++
		}
		sum += p
		if p < st.PSNRMinDB {
			st.PSNRMinDB = p
		}
	}
	st.PSNRMeanDB = sum / float64(len(outputs))
	return st
}
