package segment

import (
	"fmt"

	"archivecheck/internal/clock"
	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
)

// CheckLayerBoundaries fans in all layer reports and returns the maximum
// pairwise boundary difference on the shared clock. A difference greater than
// one shared tick fails the boundary stage while leaving the journal intact.
func CheckLayerBoundaries(layers []model.LayerReport) (clock.Tick, error) {
	max := clock.Tick(0)
	for i := 0; i < FragmentsPerLayer; i++ {
		var starts, ends []clock.Tick
		for _, l := range layers {
			if i >= len(l.Boundaries) {
				return 0, apperrors.E(apperrors.KindBoundary,
					"layer is missing fragment boundary").WithBitrate(l.Bitrate)
			}
			starts = append(starts, clock.Tick(l.Boundaries[i].StartTick))
			ends = append(ends, clock.Tick(l.Boundaries[i].EndTick))
		}
		if d := clock.MaxDiff(starts...); d > max {
			max = d
		}
		if d := clock.MaxDiff(ends...); d > max {
			max = d
		}
	}
	if max > 1 {
		return max, apperrors.E(apperrors.KindBoundary,
			fmt.Sprintf("cross-layer boundary skew %d ticks exceeds one shared tick", max))
	}
	return max, nil
}

// IntroduceSkew is a deterministic test helper that moves one boundary of one
// layer by more than one tick.
func IntroduceSkew(layers []model.LayerReport, bitrate int64, fragment int, delta int64) []model.LayerReport {
	out := append([]model.LayerReport(nil), layers...)
	for i := range out {
		if out[i].Bitrate == bitrate && fragment < len(out[i].Boundaries) {
			out[i].Boundaries[fragment].StartTick += delta
		}
	}
	return out
}
