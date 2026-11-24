package remux

import (
	"mediapipeline/internal/metadata"
)

// buildEmsgBoxes emits one top-level emsg per merged emsg event (version 1,
// absolute presentation time).  Events carried by samples (subtitles,
// chapters, ID3 and SEI) are preserved by copying those samples verbatim, so
// writing them again as emsg would duplicate them on re-parse.
func buildEmsgBoxes(merged metadata.Merged, timescale uint32) [][]byte {
	out := make([][]byte, 0, len(merged.Events))
	for _, e := range merged.Events {
		if e.Kind != metadata.KindEmsg {
			continue
		}
		ticks, err := eventTime(e.Time, timescale)
		if err != nil {
			continue
		}
		out = append(out, emsgBox(e, ticks, timescale))
	}
	return out
}

func emsgBox(e metadata.Event, ticks int64, timescale uint32) []byte {
	scheme := e.Scheme
	value := e.Value
	if scheme == "" {
		scheme = "urn:mediapipeline:" + string(e.Kind)
	}
	var b boxBuilder
	off := b.start("emsg")
	b.full(1, 0)
	b.u32(timescale)
	b.u64(uint64(ticks))
	b.u32(0) // event duration
	b.u32(0) // id
	b.str(scheme)
	b.u8(0)
	b.str(value)
	b.u8(0)
	b.raw(e.Data)
	b.finish(off)
	return b.bytes()
}

// buildChapterUdta writes Apple-style chapter entries so chapter events
// survive remux with their exact start time.
func buildChapterUdta(events metadata.Merged, timescale uint32) []byte {
	return chplBox(events.Events, timescale)
}

func chplBox(events []metadata.Event, timescale uint32) []byte {
	var chapters []metadata.Event
	for _, e := range events {
		if e.Kind == metadata.KindChapter {
			chapters = append(chapters, e)
		}
	}
	if len(chapters) == 0 {
		return nil
	}
	var ch boxBuilder
	off := ch.start("chpl")
	ch.full(0, 0)
	ch.u8(0) // reserved
	ch.u32(uint32(len(chapters)))
	for _, e := range chapters {
		ticks, err := eventTime(e.Time, timescale)
		if err != nil {
			continue
		}
		ch.u64(uint64(ticks))
		ch.u8(1)
		ch.str(string(e.Data))
		ch.u8(0)
	}
	ch.finish(off)

	var udta boxBuilder
	udtaOff := udta.start("udta")
	udta.raw(ch.bytes())
	udta.finish(udtaOff)
	return udta.bytes()
}
