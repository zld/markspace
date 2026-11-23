package hls

import (
	"mediaqc/internal/clock"
	"mediaqc/internal/model"
)

// Events converts parsed segments into the pipeline's unified event stream.
// Missing references are returned as events flagged via a model error so the
// pipeline can record the gap without aborting the whole material.
func (p *Playlist) Events(materialID, trackID string, kind model.TrackKind) ([]model.Event, []error) {
	tb, err := clock.New(1000) // EXTINF durations are kept in milliseconds
	if err != nil {
		return nil, []error{model.NewError(model.CodeZeroTimescale, model.StageParse, materialID, trackID, err.Error())}
	}
	var (
		events []model.Event
		errs   []error
		offset uint64
	)
	for _, seg := range p.Segments {
		ev := model.Event{
			MaterialID:    materialID,
			TrackID:       trackID,
			Kind:          kind,
			Sequence:      seg.Sequence,
			Discontinuity: seg.Discontinuity,
			Timebase:      tb,
			Start:         tb.At(int64(offset)),
			Duration:      tb.At(int64(seg.DurationTicks)),
			PTS:           tb.At(int64(offset)),
			Origin:        seg.URI,
			Size:          len(seg.URI),
		}
		if seg.Missing {
			errs = append(errs, model.NewError(model.CodeMissingReference, model.StageParse,
				materialID, trackID, "segment reference missing: "+seg.URI))
		}
		events = append(events, ev)
		offset += seg.DurationTicks
	}
	return events, errs
}
