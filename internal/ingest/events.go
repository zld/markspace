package ingest

import (
	"sort"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// EventInput carries the raw bytes of every timed-metadata carrier for one
// run. All fields are optional; a nil field contributes no events.
type EventInput struct {
	// WebVTT is a WebVTT document.
	WebVTT []byte
	// Chapters is a simple CHAPTERxx= / CHAPTERxxNAME= list.
	Chapters []byte
	// ID3 carries one ID3 tag per payload, already split from RTP.
	ID3 [][]byte
	// ID3Ticks are the RTP timestamps for the ID3 payloads, at ID3Timescale.
	ID3Ticks     []int64
	ID3Timescale uint32
	// Emsg carries emsg payloads and their media times.
	Emsg [][]byte
	// SEI carries raw access units that may contain SEI messages.
	SEI [][]byte
	// SubtitleTimescale times every subtitle cue (default 1000).
	SubtitleTimescale uint32
	ChapterTimescale  uint32
}

// EventResult is the output of metadata recovery.
type EventResult struct {
	Events    []model.Event
	Decisions []clock.Decision
	Errors    []*model.Error
}

// ParseEvents recovers all timed metadata. Each carrier gets its own repairer
// so that a fault on one track cannot shift another track's timeline.
func ParseEvents(in EventInput) (EventResult, error) {
	var out EventResult
	if in.SubtitleTimescale == 0 {
		in.SubtitleTimescale = 1000
	}
	if in.ChapterTimescale == 0 {
		in.ChapterTimescale = 1000
	}
	if in.ID3Timescale == 0 {
		in.ID3Timescale = 90000
	}

	if len(in.WebVTT) > 0 {
		evs, decs, errs, err := subtitleEvents(in)
		out.Events = append(out.Events, evs...)
		out.Decisions = append(out.Decisions, decs...)
		out.Errors = append(out.Errors, errs...)
		if err != nil {
			return out, err
		}
	}
	if len(in.Chapters) > 0 {
		evs, decs, errs, err := chapterEvents(in)
		out.Events = append(out.Events, evs...)
		out.Decisions = append(out.Decisions, decs...)
		out.Errors = append(out.Errors, errs...)
		if err != nil {
			return out, err
		}
	}
	if len(in.ID3) > 0 {
		evs, decs, errs, err := id3Events(in)
		out.Events = append(out.Events, evs...)
		out.Decisions = append(out.Decisions, decs...)
		out.Errors = append(out.Errors, errs...)
		if err != nil {
			return out, err
		}
	}
	if len(in.Emsg) > 0 {
		evs, decs, errs, err := emsgEvents(in)
		out.Events = append(out.Events, evs...)
		out.Decisions = append(out.Decisions, decs...)
		out.Errors = append(out.Errors, errs...)
		if err != nil {
			return out, err
		}
	}
	if len(in.SEI) > 0 {
		evs, decs, errs, err := seiEvents(in)
		out.Events = append(out.Events, evs...)
		out.Decisions = append(out.Decisions, decs...)
		out.Errors = append(out.Errors, errs...)
		if err != nil {
			return out, err
		}
	}

	SortEvents(out.Events)
	for _, e := range out.Errors {
		if !e.Recoverable {
			return out, e
		}
	}
	return out, nil
}

// SortEvents orders events deterministically by repaired start, then kind,
// then track, then ID.
func SortEvents(evs []model.Event) {
	sort.SliceStable(evs, func(i, j int) bool {
		a := modeRational(evs[i].Time.RepairedTime)
		b := modeRational(evs[j].Time.RepairedTime)
		if c := a.Cmp(b); c != 0 {
			return c < 0
		}
		if evs[i].Kind != evs[j].Kind {
			return evs[i].Kind < evs[j].Kind
		}
		if evs[i].Track != evs[j].Track {
			return evs[i].Track < evs[j].Track
		}
		return evs[i].ID < evs[j].ID
	})
}

func modeRational(t model.RationalTime) clock.Rational {
	r, err := clock.New(t.Num, t.Den)
	if err != nil {
		return clock.Zero
	}
	return r
}
