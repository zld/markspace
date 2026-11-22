package ingest

import (
	"fmt"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

func subtitleEvents(in EventInput) ([]model.Event, []clock.Decision, []*model.Error, error) {
	cues, parseErr := ParseWebVTT(in.WebVTT)
	var errs []*model.Error
	if parseErr != nil {
		errs = append(errs, model.WrapError(model.TrackSubtitles, model.StageParse, model.CodeMalformedMetadata, true, parseErr,
			"WebVTT cue parse recovered with %d cues", len(cues)))
	}
	if len(cues) == 0 {
		e := model.NewError(model.TrackSubtitles, model.StageParse, model.CodeMissingPayload, false,
			"WebVTT document contains no usable cues")
		return nil, nil, append(errs, e), e
	}
	r, err := clock.NewRepairer(model.TrackSubtitles, clock.RepairOptions{
		Timescale: in.SubtitleTimescale, Modulus: 0, JitterTicks: 1,
	})
	if err != nil {
		return nil, nil, errs, err
	}
	var evs []model.Event
	for i, cue := range cues {
		startTicks, err := msToTicks(cue.StartMS, in.SubtitleTimescale)
		if err != nil {
			return evs, r.Decisions(), errs, err
		}
		endTicks, err := msToTicks(cue.EndMS, in.SubtitleTimescale)
		if err != nil {
			return evs, r.Decisions(), errs, err
		}
		ts, err := r.Decode(startTicks)
		if err != nil {
			e := model.WrapError(model.TrackSubtitles, model.StageMetadata, model.CodeBackwardTimestamp, false, err,
				"subtitle cue %q", cue.ID)
			return evs, r.Decisions(), append(errs, e), e
		}
		evs = append(evs, model.Event{
			Kind:     model.MetaSubtitle,
			Track:    model.TrackSubtitles,
			ID:       cueID(cue, i),
			Time:     ts,
			Duration: clock.ToModel(durationOf(endTicks-startTicks, in.SubtitleTimescale)),
			ValidEnd: r.TimeAt(endTicks),
			Text:     cue.Text,
			Scheme:   "vtt",
		})
	}
	return evs, r.Decisions(), errs, nil
}

func cueID(cue Cue, i int) string {
	if cue.ID != "" {
		return cue.ID
	}
	return fmt.Sprintf("cue-%04d", i)
}

func durationOf(ticks int64, timescale uint32) clock.Rational {
	r, err := clock.FromTicks(ticks, timescale)
	if err != nil {
		return clock.Zero
	}
	return r
}

func chapterEvents(in EventInput) ([]model.Event, []clock.Decision, []*model.Error, error) {
	chapters, parseErr := ParseChapters(in.Chapters)
	var errs []*model.Error
	if parseErr != nil {
		errs = append(errs, model.WrapError(model.TrackChapters, model.StageParse, model.CodeMalformedMetadata, true, parseErr,
			"chapter list parse"))
	}
	if len(chapters) == 0 {
		e := model.NewError(model.TrackChapters, model.StageParse, model.CodeMissingPayload, false, "no chapters")
		return nil, nil, append(errs, e), e
	}
	r, err := clock.NewRepairer(model.TrackChapters, clock.RepairOptions{
		Timescale: in.ChapterTimescale, Modulus: 0, JitterTicks: 1,
	})
	if err != nil {
		return nil, nil, errs, err
	}
	var evs []model.Event
	for i, ch := range chapters {
		ticks, err := msToTicks(ch.StartMS, in.ChapterTimescale)
		if err != nil {
			return evs, r.Decisions(), errs, err
		}
		ts, err := r.Decode(ticks)
		if err != nil {
			e := model.WrapError(model.TrackChapters, model.StageMetadata, model.CodeBackwardTimestamp, false, err,
				"chapter %q", ch.Title)
			return evs, r.Decisions(), append(errs, e), e
		}
		evs = append(evs, model.Event{
			Kind:  model.MetaChapter,
			Track: model.TrackChapters,
			ID:    fmt.Sprintf("chapter-%02d", i),
			Time:  ts,
			Text:  ch.Title,
		})
	}
	return evs, r.Decisions(), errs, nil
}
