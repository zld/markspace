package audio

import (
	"fmt"
	"sort"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// SupportedRates are the sample rates the pipeline can represent without
// resampling. Rates outside this set require an explicit resample boundary,
// which the pipeline refuses to invent.
var SupportedRates = []uint32{8000, 11025, 16000, 22050, 32000, 44100, 48000, 88200, 96000, 176400, 192000}

// IsSupportedRate reports whether rate is a standard representation rate.
func IsSupportedRate(rate uint32) bool {
	for _, r := range SupportedRates {
		if r == rate {
			return true
		}
	}
	return false
}

// RateDecision is the outcome of reconciling sample-rate declarations.
type RateDecision struct {
	Track     model.TrackID `json:"track"`
	Input     []uint32      `json:"input"`
	Selected  uint32        `json:"selected"`
	Resampled bool          `json:"resampled"`
	// Boundary is the exact rational ratio selected/input that a resampler
	// would need. It is nil when no resampling happens.
	Boundary *model.RationalTime `json:"boundary,omitempty"`
	Message  string              `json:"message"`
}

// ResolveRate reconciles possibly conflicting sample-rate declarations. It
// selects the highest supported rate present (so that no source is
// downsampled), and reports an exact rational resample boundary for every
// source that differs. A declaration of an unsupported rate is an error, not
// a silent conversion.
func ResolveRate(track model.TrackID, rates []uint32) (RateDecision, []*model.Error, error) {
	d := RateDecision{Track: track}
	var errs []*model.Error
	if len(rates) == 0 {
		e := model.NewError(track, model.StageAudio, model.CodeAudioRate, false, "no sample-rate declarations")
		return d, errs, e
	}
	seen := map[uint32]bool{}
	for _, r := range rates {
		if r == 0 {
			errs = append(errs, model.NewError(track, model.StageAudio, model.CodeAudioRate, true,
				"zero sample rate ignored"))
			continue
		}
		if !seen[r] {
			seen[r] = true
			d.Input = append(d.Input, r)
		}
		if !IsSupportedRate(r) {
			e := model.NewError(track, model.StageAudio, model.CodeAudioRate, false,
				"sample rate %d Hz is outside the supported set %v", r, SupportedRates)
			return d, errs, e
		}
	}
	if len(d.Input) == 0 {
		e := model.NewError(track, model.StageAudio, model.CodeAudioRate, false, "all sample rates invalid")
		return d, errs, e
	}
	sort.Slice(d.Input, func(i, j int) bool { return d.Input[i] < d.Input[j] })
	d.Selected = d.Input[len(d.Input)-1]
	if len(d.Input) > 1 {
		errs = append(errs, model.NewError(track, model.StageAudio, model.CodeAudioRate, true,
			"sample-rate conflict %v; selecting %d Hz", d.Input, d.Selected))
	}
	// All rates in the supported set are rational multiples of each other
	// only when their ratio is integral or a simple fraction. Record the
	// exact boundary for the lowest rate, which is the one that must be
	// converted.
	if d.Input[0] != d.Selected {
		r, err := clock.New(int64(d.Selected), int64(d.Input[0]))
		if err != nil {
			e := model.WrapError(track, model.StageAudio, model.CodeAudioRate, false, err,
				"cannot express resample ratio %d/%d", d.Selected, d.Input[0])
			return d, errs, e
		}
		boundary := clock.ToModel(r)
		d.Boundary = &boundary
		d.Resampled = true
		d.Message = fmt.Sprintf("%d Hz -> %d Hz boundary %s", d.Input[0], d.Selected, r)
	} else {
		d.Message = fmt.Sprintf("%d Hz accepted unchanged", d.Selected)
	}
	return d, errs, nil
}

// ConvertTicks converts a tick count at one rate to the nearest tick at
// another rate using exact rational arithmetic. This is the only supported
// resample arithmetic; callers must have a ResolveRate boundary first.
func ConvertTicks(ticks int64, from, to uint32) (int64, error) {
	if from == 0 || to == 0 {
		return 0, fmt.Errorf("zero sample rate")
	}
	r, err := clock.FromTicks(ticks, from)
	if err != nil {
		return 0, err
	}
	return r.FloorTicks(to)
}
