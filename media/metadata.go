package media

import (
	"fmt"

	"mediad/timeline"
)

// EventSync is a fundamental unit of metadata parsing: a metadata payload, the
// track time base used to place it, and its source position.
type EventSync struct {
	Data       []byte
	Source     timeline.Source
	Base       timeline.Base
	Ticks      int64 // time of the event in Base
	Duration   timeline.Rational
	Scheme     string
	FrameIndex int
}

// ParseSubtitleSamples turns tx3g samples into subtitle events. Each sample is a
// 2-byte big-endian text length followed by UTF-8 text; a length running past
// the sample, or zero-length text, produces a located issue and the sample is
// skipped rather than published.
func ParseSubtitleSamples(samples [][]byte, trackID uint32, path string, base timeline.Base, ticks []int64, durations []timeline.Rational) ([]timeline.Event, timeline.Issues) {
	var events []timeline.Event
	var issues timeline.Issues
	for i, data := range samples {
		src := timeline.Source{File: path, TrackID: trackID, FrameIndex: i}
		if i >= len(ticks) {
			issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeInvalidTime,
				File: path, TrackID: trackID, FrameIndex: i, Message: "subtitle sample has no timestamp"})
			continue
		}
		if len(data) < 2 {
			issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeTruncated,
				File: path, TrackID: trackID, FrameIndex: i, Ticks: ticks[i], TimeScale: base.TimeScale,
				Message: fmt.Sprintf("subtitle sample is %d bytes, needs a 2-byte length prefix", len(data))})
			continue
		}
		n := int(data[0])<<8 | int(data[1])
		if 2+n > len(data) {
			issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeInvalidLength,
				File: path, TrackID: trackID, FrameIndex: i, Ticks: ticks[i], TimeScale: base.TimeScale,
				Message: fmt.Sprintf("subtitle length %d exceeds sample payload %d", n, len(data)-2)})
			continue
		}
		text := data[2 : 2+n]
		if n == 0 {
			continue // empty cue: nothing to publish
		}
		dur := timeline.Rational{}
		if i < len(durations) {
			dur = durations[i]
		}
		ev, err := timeline.NewEvent(timeline.KindSubtitle, timeline.Timestamp{Ticks: ticks[i], Base: base}, dur,
			"tx3g", text, src)
		if err != nil {
			issues.AddError(timeline.CodeInvalidTime, err)
			continue
		}
		ev.Source.ByteLength = int64(len(data))
		events = append(events, ev)
	}
	return events, issues
}

// ParseChpl parses a Nero chapter list (the chpl box payload after its header).
// The format is: 4-byte reserved, 1-byte count, then per chapter an 8-byte
// start time in 100 ns units and a 1-byte title length plus title bytes.
// Chapters are placed on the shared timeline in seconds.
func ParseChpl(data []byte, path string, trackID uint32) ([]timeline.Event, timeline.Issues) {
	var events []timeline.Event
	var issues timeline.Issues
	if len(data) < 5 {
		issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeTruncated,
			File: path, TrackID: trackID, Message: fmt.Sprintf("chpl payload is %d bytes, needs at least 5", len(data))})
		return nil, issues
	}
	count := int(data[4])
	pos := 5
	for i := 0; i < count; i++ {
		if pos+9 > len(data) {
			issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeTruncated,
				File: path, TrackID: trackID, FrameIndex: i, Message: fmt.Sprintf("chapter %d header runs past payload", i)})
			break
		}
		start100ns := int64(beU64(data[pos:]))
		titleLen := int(data[pos+8])
		pos += 9
		if pos+titleLen > len(data) {
			issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeInvalidLength,
				File: path, TrackID: trackID, FrameIndex: i, Message: fmt.Sprintf("chapter %d title length %d exceeds payload", i, titleLen)})
			break
		}
		title := data[pos : pos+titleLen]
		pos += titleLen
		// chpl times are absolute in 100 ns ticks, independent of the track
		// time base; convert to seconds exactly.
		secs, err := timeline.NewRational(start100ns, 10_000_000)
		if err != nil {
			issues.AddError(timeline.CodeInvalidTime, err)
			continue
		}
		ev := timeline.Event{
			Kind:         timeline.KindChapter,
			Time:         timeline.Timestamp{Ticks: start100ns, Base: timeline.Base{Name: "chpl100ns", TimeScale: 10_000_000}},
			Presentation: secs,
			Scheme:       "chpl",
			Payload:      append([]byte(nil), title...),
			Source:       timeline.Source{File: path, TrackID: trackID, FrameIndex: i},
		}
		events = append(events, ev)
	}
	return events, issues
}
