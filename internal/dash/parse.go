// Package dash parses offline DASH Media Presentation fixtures into the
// unified model.SegmentDescriptor stream. Every representation carries its
// own timescale; timestamps are mapped onto the shared nanosecond base and
// zero timescales, overflows and non-positive durations are rejected with
// classified, locatable errors instead of being silently coerced.
package dash

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"mediasync/internal/model"
)

// Representation is one bitrate layer inside a DASH period.
type Representation struct {
	ID        string
	Kind      model.AssetKind
	Priority  model.Priority
	Timescale uint32
	Segments  []model.SegmentDescriptor
}

// Period groups representations that share one DASH period timeline.
type Period struct {
	ID              int
	Representations []*Representation
}

// Manifest is the parsed presentation: periods in order, plus every
// non-fatal parse error (one bad representation never aborts the others).
type Manifest struct {
	Periods []*Period
	Errors  []*model.MediaError
}

// Parse decodes the deterministic line-based fixture format:
//
//	PERIOD <id>
//	REP <id> <kind> <priority> <timescale>
//	SEG <index> <startInScale> <durationInScale> [keyframe]
//
// Blank lines and '#' comments are ignored. Invalid lines are returned as
// classified errors with their 1-based line number and parsing continues.
func Parse(data []byte) *Manifest {
	m := &Manifest{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var period *Period
	var rep *Representation
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "PERIOD":
			id, err := parseField(fields, 1, lineNo)
			if err != nil {
				m.Errors = append(m.Errors, err)
				period, rep = nil, nil
				continue
			}
			period = &Period{ID: int(id)}
			m.Periods = append(m.Periods, period)
			rep = nil
		case "REP":
			if period == nil {
				m.Errors = append(m.Errors, model.NewError(model.KindMalformedMP4, "dash",
					int64(lineNo), "REP before any PERIOD: %q", line))
				rep = nil
				continue
			}
			r, err := parseRep(fields, lineNo)
			if err != nil {
				m.Errors = append(m.Errors, err)
				rep = nil
				continue
			}
			period.Representations = append(period.Representations, r)
			rep = r
		case "SEG":
			if rep == nil {
				m.Errors = append(m.Errors, model.NewError(model.KindMalformedMP4, "dash",
					int64(lineNo), "SEG before any REP: %q", line))
				continue
			}
			seg, err := parseSeg(fields, lineNo, period.ID, rep)
			if err != nil {
				m.Errors = append(m.Errors, err)
				continue
			}
			rep.Segments = append(rep.Segments, seg)
		default:
			m.Errors = append(m.Errors, model.NewError(model.KindMalformedMP4, "dash",
				int64(lineNo), "unknown directive %q", fields[0]))
		}
	}
	if err := sc.Err(); err != nil {
		m.Errors = append(m.Errors, model.NewError(model.KindMalformedMP4, "dash",
			int64(lineNo), "scanner failure: %v", err))
	}
	return m
}

func parseField(fields []string, idx, lineNo int) (int64, *model.MediaError) {
	if idx >= len(fields) {
		return 0, model.NewError(model.KindMalformedMP4, "dash", int64(lineNo),
			"missing field %d in %q", idx, strings.Join(fields, " "))
	}
	v, err := strconv.ParseInt(fields[idx], 10, 64)
	if err != nil {
		return 0, model.NewError(model.KindMalformedMP4, "dash", int64(lineNo),
			"invalid integer %q: %v", fields[idx], err)
	}
	return v, nil
}

func parseRep(fields []string, lineNo int) (*Representation, *model.MediaError) {
	if len(fields) < 5 {
		return nil, model.NewError(model.KindMalformedMP4, "dash", int64(lineNo),
			"REP needs id kind priority timescale, got %q", strings.Join(fields, " "))
	}
	kind, ok := parseKind(fields[2])
	if !ok {
		return nil, model.NewError(model.KindMalformedMP4, "dash", int64(lineNo),
			"unknown asset kind %q", fields[2])
	}
	pri, err := parseField(fields, 3, lineNo)
	if err != nil {
		return nil, err
	}
	ts, err := parseField(fields, 4, lineNo)
	if err != nil {
		return nil, err
	}
	if ts <= 0 {
		return nil, model.NewError(model.KindInvalidTimescale, "dash", int64(lineNo),
			"representation %q declares timescale %d", fields[1], ts)
	}
	return &Representation{ID: fields[1], Kind: kind,
		Priority: model.Priority(pri), Timescale: uint32(ts)}, nil
}

func parseSeg(fields []string, lineNo, periodID int, rep *Representation) (model.SegmentDescriptor, *model.MediaError) {
	if len(fields) < 4 {
		return model.SegmentDescriptor{}, model.NewError(model.KindMalformedMP4, "dash",
			int64(lineNo), "SEG needs index start duration, got %q", strings.Join(fields, " "))
	}
	idx, err := parseField(fields, 1, lineNo)
	if err != nil {
		return model.SegmentDescriptor{}, err
	}
	start, err := parseField(fields, 2, lineNo)
	if err != nil {
		return model.SegmentDescriptor{}, err
	}
	dur, err := parseField(fields, 3, lineNo)
	if err != nil {
		return model.SegmentDescriptor{}, err
	}
	if start < 0 {
		return model.SegmentDescriptor{}, model.NewError(model.KindNegativeDuration, "dash",
			int64(lineNo), "rep %s segment %d has negative start %d", rep.ID, idx, start)
	}
	startNS, sErr := model.ScaleToNanoseconds(start, rep.Timescale)
	if sErr != nil {
		return model.SegmentDescriptor{}, annotate(asMedia(sErr), lineNo)
	}
	durNS, dErr := model.ScaleDuration(dur, rep.Timescale)
	if dErr != nil {
		return model.SegmentDescriptor{}, annotate(asMedia(dErr), lineNo)
	}
	keyframe := len(fields) > 4 && fields[4] == "keyframe"
	return model.SegmentDescriptor{
		Source: "dash", Representation: rep.ID, PeriodID: periodID, Index: int(idx),
		Kind: rep.Kind, Keyframe: keyframe, StartNS: startNS, DurationNS: durNS,
		Timescale: rep.Timescale, StartInScale: start, DurationScale: dur,
	}, nil
}

func annotate(err *model.MediaError, lineNo int) *model.MediaError {
	return model.NewError(err.Kind, "dash", int64(lineNo), "%s", err.Context)
}

// asMedia converts errors produced by model helpers back into the locatable
// taxonomy; the model helpers always return *model.MediaError.
func asMedia(err error) *model.MediaError {
	if me, ok := err.(*model.MediaError); ok {
		return me
	}
	return model.NewError(model.KindMalformedMP4, "dash", 0, "%v", err)
}

func parseKind(s string) (model.AssetKind, bool) {
	switch s {
	case "dialogue":
		return model.KindDialogue, true
	case "subtitle":
		return model.KindSubtitle, true
	case "sfx":
		return model.KindSFX, true
	case "video":
		return model.KindVideo, true
	default:
		return 0, false
	}
}
