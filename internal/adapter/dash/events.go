package dash

import (
	"mediaqc/internal/clock"
	"mediaqc/internal/model"
)

// Events converts every representation's segments into unified events, using
// the exact (possibly fractional) representation timescale.
func (m *Manifest) Events(materialID string) ([]model.Event, []error) {
	var events []model.Event
	var errs []error
	for _, period := range m.Periods {
		for _, as := range period.AdaptationSets {
			kind := model.TrackVideo
			if as.ContentType == "audio" {
				kind = model.TrackAudio
			}
			for _, rep := range period.RepresentationsOf(as) {
				tb, err := representationTimebase(rep)
				if err != nil {
					errs = append(errs, model.Wrap(err, model.CodeZeroTimescale, model.StageParse,
						materialID, rep.ID, "invalid representation timescale"))
					continue
				}
				for _, seg := range rep.Segments {
					ev := model.Event{
						MaterialID: materialID,
						TrackID:    rep.ID,
						Kind:       kind,
						Sequence:   seg.Index,
						Timebase:   tb,
						Start:      tb.At(seg.Start),
						Duration:   tb.At(seg.Duration),
						PTS:        tb.At(seg.Start),
						Origin:     seg.Media,
						Size:       len(seg.Media),
					}
					if seg.Missing {
						errs = append(errs, model.NewError(model.CodeMissingReference, model.StageParse,
							materialID, rep.ID, "missing segment "+seg.Media))
					}
					events = append(events, ev)
				}
			}
		}
	}
	return events, errs
}

// RepresentationsOf returns the representations inside an adaptation set.
func (p Period) RepresentationsOf(as AdaptationSet) []Representation {
	return as.Representations
}

func representationTimebase(rep Representation) (clock.Timebase, error) {
	if rep.NonInteger {
		r, err := clock.NewRational(rep.Num, rep.Den)
		if err != nil {
			return clock.Timebase{}, err
		}
		return clock.NewFromRational(r)
	}
	return clock.New(rep.Timescale)
}
