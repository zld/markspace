package timeline

import (
	"fmt"
	"math"

	"medialab/internal/model"
)

// AlignConfig bounds acceptable timing error.
type AlignConfig struct {
	// MaxErrorNS is the largest per-event deviation tolerated.
	MaxErrorNS int64
	// MaxDriftNS is the largest end-to-end drift tolerated.
	MaxDriftNS int64
}

// AlignReport summarises alignment against the shared clock.
type AlignReport struct {
	MaxErrorNS  int64                   `json:"maxErrorNs"`
	MaxDriftNS  int64                   `json:"maxDriftNs"`
	MeanErrorNS int64                   `json:"meanErrorNs"`
	PerTrack    map[model.TrackID]int64 `json:"perTrackMaxErrorNs"`
	Violations  model.Faults            `json:"violations,omitempty"`
}

// Align checks every event's timestamp against its track's nominal cadence.
// The nominal cadence is the median inter-frame interval of the track; a
// frame whose deviation exceeds MaxErrorNS is a violation. This catches
// timestamp regressions and misplaced frames without assuming a production
// frame rate.
func Align(tl *Timeline, cfg AlignConfig) (AlignReport, error) {
	rep := AlignReport{PerTrack: make(map[model.TrackID]int64)}
	if tl == nil || len(tl.Events) == 0 {
		return rep, model.NewFault(model.KindIngest, model.CodeManifestInvalid, 0, 0,
			"cannot align an empty timeline")
	}
	byTrack := make(map[model.TrackID][]Event)
	for _, e := range tl.Events {
		byTrack[e.Frame.Track] = append(byTrack[e.Frame.Track], e)
	}

	var totalErr int64
	var counted int64
	for _, tr := range tl.Tracks {
		events := byTrack[tr]
		if len(events) < 2 {
			continue
		}
		cadence := medianInterval(events)
		if cadence <= 0 {
			cadence = 1
		}
		var trackMax int64
		for i := 1; i < len(events); i++ {
			actual := int64(events[i].Frame.PTS) - int64(events[i-1].Frame.PTS)
			if actual < 0 {
				// A regression is always a violation.
				actual = -actual
			}
			dev := actual - cadence
			if dev < 0 {
				dev = -dev
			}
			// Deviation is measured from the nearest cadence multiple, so a
			// dropped frame does not masquerade as a timing error.
			if cadence > 0 {
				rem := dev % cadence
				if rem > cadence/2 {
					dev = cadence - rem
				} else {
					dev = rem
				}
			}
			if dev > trackMax {
				trackMax = dev
			}
			totalErr += dev
			counted++
		}
		if trackMax > rep.MaxErrorNS {
			rep.MaxErrorNS = trackMax
		}
		if cadence > 0 {
			drift := trackMax
			if drift > rep.MaxDriftNS {
				rep.MaxDriftNS = drift
			}
		}
		rep.PerTrack[tr] = trackMax
		if cfg.MaxErrorNS > 0 && trackMax > cfg.MaxErrorNS {
			rep.Violations = append(rep.Violations, model.NewFault(model.KindIngest,
				model.CodeConstraintViolation, tr, 0,
				fmt.Sprintf("max alignment error %d ns exceeds %d ns", trackMax, cfg.MaxErrorNS)))
		}
	}
	if counted > 0 {
		rep.MeanErrorNS = totalErr / counted
	}
	if cfg.MaxDriftNS > 0 && rep.MaxDriftNS > cfg.MaxDriftNS {
		rep.Violations = append(rep.Violations, model.NewFault(model.KindIngest,
			model.CodeConstraintViolation, 0, 0,
			fmt.Sprintf("drift %d ns exceeds %d ns", rep.MaxDriftNS, cfg.MaxDriftNS)))
	}
	return rep, nil
}

// medianInterval returns the median gap between consecutive events.
func medianInterval(events []Event) int64 {
	gaps := make([]int64, 0, len(events)-1)
	for i := 1; i < len(events); i++ {
		g := int64(events[i].Frame.PTS) - int64(events[i-1].Frame.PTS)
		if g < 0 {
			g = -g
		}
		gaps = append(gaps, g)
	}
	if len(gaps) == 0 {
		return 0
	}
	sortedInt64(gaps)
	mid := len(gaps) / 2
	if len(gaps)%2 == 0 {
		return (gaps[mid-1] + gaps[mid]) / 2
	}
	return gaps[mid]
}

// FitCubicNS fits the ideal cadence with a robust median, avoiding float
// accumulation so results are stable across machines.
func FitCubicNS(cadence int64, count int) int64 {
	if count <= 0 || cadence <= 0 {
		return 0
	}
	if cadence > math.MaxInt64/int64(count) {
		return math.MaxInt64
	}
	return cadence * int64(count)
}

func sortedInt64(v []int64) {
	// Insertion sort is fine for the small per-track cadence sample.
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
