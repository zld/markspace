package ingest

import (
	"fmt"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

func id3Events(in EventInput) ([]model.Event, []clock.Decision, []*model.Error, error) {
	if len(in.ID3Ticks) != 0 && len(in.ID3Ticks) != len(in.ID3) {
		e := model.NewError(model.TrackID3, model.StageParse, model.CodeInvalidParameter, false,
			"%d ID3 payloads but %d timestamps", len(in.ID3), len(in.ID3Ticks))
		return nil, nil, []*model.Error{e}, e
	}
	r, err := clock.NewRepairer(model.TrackID3, clock.RepairOptions{
		Timescale: in.ID3Timescale, Modulus: 0, JitterTicks: 1,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	var errs []*model.Error
	var evs []model.Event
	for i, payload := range in.ID3 {
		tag, perr := ParseID3(payload)
		if perr != nil {
			errs = append(errs, model.WrapError(model.TrackID3, model.StageParse, model.CodeMalformedMetadata, true, perr,
				"ID3 tag %d decoded with %d frames", i, len(tag.Frames)))
		}
		title, _ := tag.Text("TIT2")
		rawTicks := int64(-1)
		if len(in.ID3Ticks) > 0 {
			rawTicks = in.ID3Ticks[i]
		}
		ts := model.TimeStamp{RawTicks: rawTicks, State: model.DecodeMissing}
		if rawTicks >= 0 {
			decoded, derr := r.Decode(rawTicks)
			if derr != nil {
				e := model.WrapError(model.TrackID3, model.StageMetadata, model.CodeBackwardTimestamp, false, derr,
					"ID3 tag %d", i)
				return evs, r.Decisions(), append(errs, e), e
			}
			ts = decoded
		} else {
			errs = append(errs, model.NewError(model.TrackID3, model.StageParse, model.CodeMissingPayload, true,
				"ID3 tag %d has no RTP timestamp; kept with missing time", i))
		}
		evs = append(evs, model.Event{
			Kind:    model.MetaID3,
			Track:   model.TrackID3,
			ID:      fmt.Sprintf("id3-%03d", i),
			Time:    ts,
			Text:    title,
			Payload: payload,
		})
	}
	return evs, r.Decisions(), errs, nil
}

func emsgEvents(in EventInput) ([]model.Event, []clock.Decision, []*model.Error, error) {
	r, err := clock.NewRepairer(model.TrackEmsg, clock.RepairOptions{
		Timescale: 1000, Modulus: 0, JitterTicks: 1,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	var errs []*model.Error
	var evs []model.Event
	for i, payload := range in.Emsg {
		e, perr := ParseEmsg(payload)
		if perr != nil {
			errs = append(errs, model.WrapError(model.TrackEmsg, model.StageParse, model.CodeMalformedMetadata, true, perr,
				"emsg %d", i))
			continue
		}
		if e.Timescale == 0 {
			errs = append(errs, model.NewError(model.TrackEmsg, model.StageParse, model.CodeMalformedMetadata, true,
				"emsg %d has zero timescale; skipped", i))
			continue
		}
		ms, err := clock.New(e.TimeTicks, int64(e.Timescale))
		if err != nil {
			return evs, r.Decisions(), errs, err
		}
		ticks, err := clock.TicksAt(ms, 1000)
		if err != nil {
			return evs, r.Decisions(), errs, err
		}
		ts, err := r.Decode(ticks)
		if err != nil {
			pe := model.WrapError(model.TrackEmsg, model.StageMetadata, model.CodeBackwardTimestamp, false, err,
				"emsg %d", i)
			return evs, r.Decisions(), append(errs, pe), pe
		}
		evs = append(evs, model.Event{
			Kind:     model.MetaEmsg,
			Track:    model.TrackEmsg,
			ID:       fmt.Sprintf("emsg-%03d", i),
			Time:     ts,
			Duration: clock.ToModel(durationOf(int64(e.Duration), e.Timescale)),
			Scheme:   e.SchemeID,
			Value:    e.Value,
			Payload:  e.Message,
		})
	}
	return evs, r.Decisions(), errs, nil
}
