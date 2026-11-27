package pixel

import "math"

// LoudnessFloorDBFS is the silence floor reported when a frame carries no
// alternating energy.
const LoudnessFloorDBFS = -120.0

// LoudnessPeakDBFS derives a repeatable loudness proxy from the pixel planes.
// The project's inputs carry no audio track, so the peak sample of a synthetic
// waveform taken as each channel's deviation from mid-grey stands in for an
// audio peak; both render and verify recompute it from decoded pixels, so a
// mismatch is a real artefact defect rather than an interpretation difference.
func LoudnessPeakDBFS(r Raw) (float64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	var peak int
	for _, v := range r.Pix {
		d := int(v) - 127
		if d < 0 {
			d = -d
		}
		if d > peak {
			peak = d
		}
	}
	if peak == 0 {
		return LoudnessFloorDBFS, nil
	}
	db := 20 * math.Log10(float64(peak)/127.5)
	if db < LoudnessFloorDBFS {
		return LoudnessFloorDBFS, nil
	}
	return db, nil
}
