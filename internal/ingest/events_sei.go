package ingest

import (
	"fmt"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

func seiEvents(in EventInput) ([]model.Event, []clock.Decision, []*model.Error, error) {
	r, err := clock.NewRepairer(model.TrackSEI, clock.RepairOptions{
		Timescale: 1000000, Modulus: 0, JitterTicks: 1,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	var errs []*model.Error
	var evs []model.Event
	n := 0
	for i, au := range in.SEI {
		msgs, perr := ParseSEI(au)
		if perr != nil {
			errs = append(errs, model.WrapError(model.TrackSEI, model.StageParse, model.CodeMalformedMetadata, true, perr,
				"SEI access unit %d", i))
		}
		stamps, serr := SEITimestamps(msgs)
		if serr != nil {
			errs = append(errs, model.WrapError(model.TrackSEI, model.StageParse, model.CodeMalformedMetadata, true, serr,
				"SEI access unit %d", i))
		}
		for _, st := range stamps {
			ts, err := r.Decode(st.TimeUS)
			if err != nil {
				pe := model.WrapError(model.TrackSEI, model.StageMetadata, model.CodeBackwardTimestamp, false, err,
					"SEI message %d", n)
				return evs, r.Decisions(), append(errs, pe), pe
			}
			evs = append(evs, model.Event{
				Kind:  model.MetaSEI,
				Track: model.TrackSEI,
				ID:    fmt.Sprintf("sei-%03d", n),
				Time:  ts,
				Text:  st.Kind,
			})
			n++
		}
	}
	return evs, r.Decisions(), errs, nil
}
