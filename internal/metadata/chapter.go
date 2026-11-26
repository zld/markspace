package metadata

import (
	"encoding/json"
	"fmt"
	"io"

	"replaypipe/internal/timebase"
)

// chapterDoc is the accepted chapter JSON document. Times may be ISO-8601
// (HH:MM:SS.mmm), SRT-style comma milliseconds or integer milliseconds.
type chapterDoc struct {
	Version  int            `json:"version"`
	Timebase string         `json:"timebase,omitempty"`
	Chapters []chapterEntry `json:"chapters"`
}

type chapterEntry struct {
	ID    string          `json:"id,omitempty"`
	Title string          `json:"title"`
	Start json.RawMessage `json:"start"`
	End   json.RawMessage `json:"end,omitempty"`
	Track uint32          `json:"track_id,omitempty"`
}

// ParseChapters decodes a chapter JSON document and normalises every chapter
// to nanoseconds. Chapters are closed by the next chapter start when no end
// is given; the final chapter must carry an explicit end.
func ParseChapters(r io.Reader, opts ParseOptions) ([]Event, *timebase.Error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var doc chapterDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, timebase.Fail(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset).
			Wrap(err, "decode chapter json")
	}
	if doc.Version != 0 && doc.Version != 1 {
		return nil, timebase.Newf(timebase.CodeUnsupportedFormat, opts.TrackID, opts.BaseOffset,
			"chapter document version %d is not supported", doc.Version)
	}
	if len(doc.Chapters) == 0 {
		return nil, timebase.Fail(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset).
			Wrap(nil, "chapter document contains no chapters")
	}
	base := timebase.Millis()
	if doc.Timebase != "" {
		b, err := timebase.ParseRate(doc.Timebase)
		if err != nil {
			return nil, timebase.Fail(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset).Wrap(err, "chapter timebase")
		}
		base = b
	}
	events := make([]Event, 0, len(doc.Chapters))
	for i, ch := range doc.Chapters {
		startTicks, e := decodeChapterTime(ch.Start, base)
		if e != nil {
			return nil, e.WithTrack(opts.TrackID).WithOffset(opts.BaseOffset)
		}
		track := opts.TrackID
		if ch.Track != 0 {
			track = ch.Track
		}
		startNS, e := base.ToNanos(startTicks, track, opts.BaseOffset)
		if e != nil {
			return nil, e
		}
		var endNS int64
		if len(ch.End) > 0 && string(ch.End) != "null" {
			endTicks, e := decodeChapterTime(ch.End, base)
			if e != nil {
				return nil, e.WithTrack(track).WithOffset(opts.BaseOffset)
			}
			endNS, e = base.ToNanos(endTicks, track, opts.BaseOffset)
			if e != nil {
				return nil, e
			}
		} else if i+1 < len(doc.Chapters) {
			nextTicks, e := decodeChapterTime(doc.Chapters[i+1].Start, base)
			if e != nil {
				return nil, e.WithTrack(track).WithOffset(opts.BaseOffset)
			}
			endNS, e = base.ToNanos(nextTicks, track, opts.BaseOffset)
			if e != nil {
				return nil, e
			}
		} else {
			return nil, timebase.Newf(timebase.CodeCorruptField, track, opts.BaseOffset,
				"final chapter %q has no end time", ch.Title)
		}
		id := ch.ID
		if id == "" {
			id = fmt.Sprintf("chapter-%04d-%s", i+1, slug(ch.Title))
		}
		ev := Event{
			ID:           id,
			Kind:         KindChapter,
			TrackID:      track,
			Source:       "chapters",
			StartNS:      startNS,
			EndNS:        endNS,
			Timebase:     base.String(),
			Text:         ch.Title,
			SourceOffset: opts.BaseOffset,
		}
		if e := ev.Validate(opts); e != nil {
			return nil, e
		}
		events = append(events, ev)
	}
	if e := ValidateOrder(events, opts); e != nil {
		return nil, e
	}
	return events, nil
}

func decodeChapterTime(raw json.RawMessage, base timebase.Base) (int64, *timebase.Error) {
	if len(raw) == 0 {
		return 0, timebase.Newf(timebase.CodeCorruptField, 0, -1, "missing chapter time")
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		ticks, err := num.Int64()
		if err != nil {
			return 0, timebase.Fail(timebase.CodeCorruptField, 0, -1).Wrap(err, "chapter time %q is not an integer", string(raw))
		}
		if ticks < 0 {
			return 0, timebase.Newf(timebase.CodeEventOutOfRange, 0, -1, "negative chapter time %d", ticks)
		}
		return ticks, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, timebase.Newf(timebase.CodeCorruptField, 0, -1, "chapter time %q is neither integer ticks nor a clock string", string(raw))
	}
	ms, e := parseClock(text)
	if e != nil {
		return 0, e
	}
	// clock strings are always milliseconds; convert to the document base.
	if base.Equal(timebase.Millis()) {
		return ms, nil
	}
	ns, e := timebase.Millis().ToNanos(ms, 0, -1)
	if e != nil {
		return 0, e
	}
	return base.FromNanos(ns, 0, -1)
}
