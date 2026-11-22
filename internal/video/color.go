// Package video resolves color metadata conflicts and validates keyframe
// placement on sample boundaries.
package video

import (
	"fmt"
	"sort"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// Resolution is the outcome of reconciling color declarations. Every source
// declaration is kept alongside the selected value and the rationale, so an
// editor can see exactly what was overridden.
type Resolution struct {
	Declarations []model.ColorDeclaration `json:"declarations"`
	Selected     model.ColorDeclaration   `json:"selected"`
	Conflicts    []Conflict               `json:"conflicts"`
	// Complete is false when no declaration specified all three parameters.
	Complete bool `json:"complete"`
}

// Conflict is one disagreement between sources on a color field.
type Conflict struct {
	Field      string            `json:"field"`
	Chosen     uint16            `json:"chosen"`
	Other      uint16            `json:"other"`
	ChosenFrom model.ColorSource `json:"chosen_from"`
	OtherFrom  model.ColorSource `json:"other_from"`
	Reason     string            `json:"reason"`
}

// Resolve reconciles color declarations. Precedence is container > bitstream
// > manifest, matching the ordering of model.ColorSource. Unspecified (zero)
// fields never win over specified ones. Every disagreement is recorded, and
// a complete declaration is preferred over a partial one.
func Resolve(track model.TrackID, decls []model.ColorDeclaration) (Resolution, []*model.Error, error) {
	var res Resolution
	var errs []*model.Error
	if len(decls) == 0 {
		e := model.NewError(track, model.StageVideo, model.CodeColorUnknown, false,
			"no color metadata found")
		return res, errs, e
	}
	sorted := append([]model.ColorDeclaration(nil), decls...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Source != sorted[j].Source {
			return sourceRank(sorted[i].Source) < sourceRank(sorted[j].Source)
		}
		return lessColor(sorted[i], sorted[j])
	})
	res.Declarations = sorted

	pick := func(get func(model.ColorDeclaration) uint16, field string) uint16 {
		var chosen uint16
		var chosenFrom model.ColorSource
		for _, d := range sorted {
			v := get(d)
			if v == 0 {
				continue
			}
			if chosen == 0 {
				chosen, chosenFrom = v, d.Source
				continue
			}
			if v != chosen {
				res.Conflicts = append(res.Conflicts, Conflict{
					Field: field, Chosen: chosen, Other: v,
					ChosenFrom: chosenFrom, OtherFrom: d.Source,
					Reason: fmt.Sprintf("%s has higher precedence than %s", chosenFrom, d.Source),
				})
				errs = append(errs, model.NewError(track, model.StageVideo, model.CodeColorUnknown, true,
					"color %s conflict: %d (%s) vs %d (%s); keeping %d",
					field, chosen, chosenFrom, v, d.Source, chosen))
			}
		}
		return chosen
	}
	res.Selected = model.ColorDeclaration{Source: model.ColorFromContainer}
	res.Selected.Primaries = pick(func(d model.ColorDeclaration) uint16 { return d.Primaries }, "primaries")
	res.Selected.Transfer = pick(func(d model.ColorDeclaration) uint16 { return d.Transfer }, "transfer")
	res.Selected.Matrix = pick(func(d model.ColorDeclaration) uint16 { return d.Matrix }, "matrix")
	for _, d := range sorted {
		if d.Primaries != 0 || d.Transfer != 0 || d.Matrix != 0 {
			res.Selected.Source = d.Source
			res.Selected.FullRange = d.FullRange
			break
		}
	}
	res.Complete = res.Selected.Primaries != 0 && res.Selected.Transfer != 0 && res.Selected.Matrix != 0
	if !res.Complete {
		errs = append(errs, model.NewError(track, model.StageVideo, model.CodeColorUnknown, true,
			"selected color metadata is incomplete (primaries=%d transfer=%d matrix=%d)",
			res.Selected.Primaries, res.Selected.Transfer, res.Selected.Matrix))
	}
	return res, errs, nil
}

func sourceRank(s model.ColorSource) int {
	switch s {
	case model.ColorFromContainer:
		return 0
	case model.ColorFromBitstream:
		return 1
	default:
		return 2
	}
}

func lessColor(a, b model.ColorDeclaration) bool {
	if a.Primaries != b.Primaries {
		return a.Primaries < b.Primaries
	}
	if a.Transfer != b.Transfer {
		return a.Transfer < b.Transfer
	}
	return a.Matrix < b.Matrix
}

// KeyframeCheck verifies that every declared keyframe falls exactly on a
// sample boundary and that the reconstructed keyframe list is monotonic.
type KeyframeCheck struct {
	Index       []int                `json:"index"`
	Times       []model.RationalTime `json:"times"`
	Missing     int                  `json:"missing"`
	OffBoundary int                  `json:"off_boundary"`
}

// ValidateKeyframes takes candidate keyframe sample indices, the full sample
// list and the timescale. An index must be a sync sample and its start time
// must equal the sample's repaired start time exactly. Sampling times are
// compared on the integer tick grid so "one clock tick" is the tolerance.
func ValidateKeyframes(track model.TrackID, samples []model.Sample, candidate []int, timescale uint32) (KeyframeCheck, []*model.Error, error) {
	var check KeyframeCheck
	var errs []*model.Error
	if len(samples) == 0 {
		e := model.NewError(track, model.StageVideo, model.CodeIndexInvalid, false, "no samples")
		return check, errs, e
	}
	seen := map[int]bool{}
	var lastTicks int64
	for i, idx := range candidate {
		if idx < 0 || idx >= len(samples) {
			e := model.NewError(track, model.StageVideo, model.CodeIndexInvalid, false,
				"keyframe index %d is outside [0,%d)", idx, len(samples))
			return check, errs, e
		}
		if seen[idx] {
			continue
		}
		seen[idx] = true
		s := samples[idx]
		if !s.Sync {
			check.OffBoundary++
			errs = append(errs, model.NewError(track, model.StageVideo, model.CodeIndexInvalid, true,
				"index %d is not a sync sample", idx))
			continue
		}
		t, err := clock.FromModel(s.Time.RepairedTime)
		if err != nil {
			return check, errs, err
		}
		ticks, err := t.FloorTicks(timescale)
		if err != nil {
			return check, errs, err
		}
		if i > 0 && ticks <= lastTicks {
			e := model.NewError(track, model.StageVideo, model.CodeIndexInvalid, false,
				"keyframe %d starts at tick %d, not after tick %d", idx, ticks, lastTicks)
			return check, errs, e
		}
		lastTicks = ticks
		check.Index = append(check.Index, idx)
		check.Times = append(check.Times, s.Time.RepairedTime)
	}
	// Sync samples not nominated are counted but not fatal: callers may
	// deliberately index a subset.
	for i, s := range samples {
		if s.Sync && !seen[i] {
			check.Missing++
		}
	}
	return check, errs, nil
}
