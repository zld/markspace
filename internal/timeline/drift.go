package timeline

import "mediapack/internal/model"

// DriftEstimate is the exact rational clock-drift of one track relative to the
// nominal rate, plus a parts-per-million figure derived without floats.
type DriftEstimate struct {
	// RateRatio = observed RTP elapsed ticks / nominal ticks over the window.
	RateRatio model.Rat `json:"rate_ratio"`
	// PPM = (RateRatio - 1) * 1_000_000, rounded.
	PPM int64 `json:"ppm"`
	// WindowSamples is how many packet pairs fed the estimate.
	WindowSamples int    `json:"window_samples"`
	AnchorFirstTS uint32 `json:"anchor_first_ts"`
	AnchorLastTS  uint32 `json:"anchor_last_ts"`
}

// EstimateDrift derives drift from the expanded timeline and an external
// nominal elapsed duration in nanoseconds (the capture device's declared
// wall-clock span). All arithmetic stays in integer rationals.
func EstimateDrift(tl *TrackTimeline, nominalMasterNS int64) (DriftEstimate, error) {
	d := DriftEstimate{WindowSamples: len(tl.Events)}
	if len(tl.Events) < 2 {
		// A single anchor cannot evidence drift; report an exact 1.0 ratio.
		d.RateRatio = model.MustRat(1, 1)
		return d, nil
	}
	first := tl.Events[0]
	last := tl.Events[len(tl.Events)-1]
	d.AnchorFirstTS = first.RawTS
	d.AnchorLastTS = last.RawTS

	// Observed elapsed RTP ticks (unwrapped, wraps already folded in).
	obsTicks := last.UnwrappedTS - first.UnwrappedTS
	if nominalMasterNS <= 0 {
		// No external reference: treat the master mapping itself as nominal.
		nominalMasterNS = last.MasterNS - first.MasterNS
	}
	if nominalMasterNS <= 0 || tl.ClockRate == 0 {
		d.RateRatio = model.MustRat(1, 1)
		return d, nil
	}
	// Nominal ticks the RTP clock *should* have advanced:
	// nominalMasterNS / 1e9 * clockRate. Kept as a checked rational product
	// so the intermediate int64 multiply cannot silently overflow.
	nsRat, err := model.NewRat(nominalMasterNS, 1_000_000_000)
	if err != nil {
		return d, err
	}
	nomTicks, err := nsRat.Mul(model.MustRat(int64(tl.ClockRate), 1))
	if err != nil {
		return d, err
	}
	// obsTicks / nomTicks, both exact rationals.
	ratio, err := model.NewRat(obsTicks, 1)
	if err != nil {
		return d, err
	}
	d.RateRatio, err = ratio.Mul(model.MustRat(nomTicks.Den, nomTicks.Num)) // reciprocal
	if err != nil {
		return d, err
	}
	// ppm = (ratio - 1) * 1e6
	one := model.MustRat(1, 1)
	diff, err := d.RateRatio.Sub(one)
	if err != nil {
		return d, err
	}
	ppmRat, err := diff.Mul(model.MustRat(1_000_000, 1))
	if err != nil {
		return d, err
	}
	d.PPM = ppmRat.Round()
	return d, nil
}
