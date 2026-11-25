// Package clock provides the single media time base used by scanning,
// planning, recovery, fan-out, and boundary fan-in.
package clock

import (
	"fmt"

	apperrors "archivecheck/internal/errors"
)

// TicksPerSecond is the shared 90 kHz media clock. One tick is the maximum
// permitted cross-representation boundary skew.
const TicksPerSecond int64 = 90_000

// Tick is a position on the shared 90 kHz timeline.
type Tick int64

// Timebase converts a track-local timescale to the shared clock.
type Timebase struct {
	Timescale uint32 `json:"timescale"`
}

// NewTimebase validates a non-zero MP4 media timescale.
func NewTimebase(timescale uint32) (Timebase, error) {
	if timescale == 0 {
		return Timebase{}, apperrors.E(apperrors.KindContainer, "media timescale is zero")
	}
	return Timebase{Timescale: timescale}, nil
}

// ToShared converts source-timescale units to shared ticks, rounding to the
// nearest tick. Callers that need exact lossless conversion should choose a
// timescale that divides 90,000.
func (tb Timebase) ToShared(sourceUnits int64) Tick {
	if tb.Timescale == uint32(TicksPerSecond) {
		return Tick(sourceUnits)
	}
	num := sourceUnits * TicksPerSecond
	den := int64(tb.Timescale)
	if num >= 0 {
		return Tick((num + den/2) / den)
	}
	return Tick((num - den/2) / den)
}

// ToSource converts shared ticks back to source-timescale units.
func (tb Timebase) ToSource(t Tick) int64 {
	if tb.Timescale == uint32(TicksPerSecond) {
		return int64(t)
	}
	return (int64(t) * int64(tb.Timescale)) / TicksPerSecond
}

// Timeline preserves the three coordinates that must never be conflated:
// raw container time, edit-list output time, and the output zero point.
type Timeline struct {
	AssetID               string `json:"asset_id"`
	Timescale             uint32 `json:"timescale"`
	ContainerStart        Tick   `json:"container_start_tick"`
	EditZeroPoint         Tick   `json:"edit_zero_point_tick"`
	EditedStart           Tick   `json:"edited_start_tick"`
	EditedDuration        Tick   `json:"edited_duration_tick"`
	NonZeroStartPreserved bool   `json:"non_zero_start_preserved"`
}

// Edited maps a container position to output/edit-list time. The zero point
// is retained explicitly instead of subtracting it and discarding its value.
func (tl Timeline) Edited(container Tick) Tick {
	return container - tl.EditZeroPoint + tl.EditedStart
}

// ValidateZeroPreservation rejects the unsafe operation of silently replacing
// a non-zero container start with zero.
func (tl Timeline) ValidateZeroPreservation() error {
	if tl.ContainerStart != 0 && !tl.NonZeroStartPreserved {
		return apperrors.E(apperrors.KindContainer,
			fmt.Sprintf("non-zero container start %d was silently zeroed", tl.ContainerStart))
	}
	return nil
}

// Abs returns the absolute tick distance between two positions.
func Abs(a, b Tick) Tick {
	if a >= b {
		return a - b
	}
	return b - a
}

// MaxDiff returns the largest absolute pairwise difference. It is used for
// both recovery positions and cross-layer boundaries.
func MaxDiff(values ...Tick) Tick {
	var max Tick
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if d := Abs(values[i], values[j]); d > max {
				max = d
			}
		}
	}
	return max
}
