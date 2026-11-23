package dash

import (
	"fmt"
	"strconv"
	"strings"

	"mediadeliver/internal/model"
)

// maxSegmentsPerRepresentation bounds timeline expansion so a corrupt repeat
// count cannot exhaust memory.
const maxSegmentsPerRepresentation = 100000

func parsePeriod(el *rawElement, path string, mp *MPD) Period {
	p := Period{ID: attrString(el, "id")}
	if a, ok := el.attrs["start"]; ok && a.value != "" {
		start, err := model.ParseISODuration(a.value)
		if err != nil {
			mp.Errors = append(mp.Errors, model.Wrap(err, model.CodeMPDInvalid, path, a.offset, model.StageParse, false))
		}
		p.StartNS = start
	}
	p.DurNS = parseDurationAttr(el, "duration", path, &mp.Errors)
	for _, as := range childElements(el, "AdaptationSet") {
		p.Reps = append(p.Reps, parseAdaptationSet(as, path, mp, p)...)
	}
	return p
}

func parseAdaptationSet(el *rawElement, path string, mp *MPD, p Period) []*Representation {
	contentType := attrString(el, "contentType")
	mime := attrString(el, "mimeType")
	asTemplate, asErrs := parseTemplate(el, path, nil)
	mp.Errors = append(mp.Errors, asErrs...)
	var reps []*Representation
	for _, rel := range childElements(el, "Representation") {
		r := &Representation{
			ID:            attrString(rel, "id"),
			MimeType:      mime,
			Codecs:        attrString(rel, "codecs"),
			ContentType:   contentType,
			PeriodID:      p.ID,
			PeriodStartNS: p.StartNS,
			PathRef:       path,
		}
		if lm := attrString(rel, "mimeType"); lm != "" {
			r.MimeType = lm
		}
		if v, ok, err := attrInt(rel, "bandwidth"); err != nil {
			mp.Errors = append(mp.Errors, err.WithPath(path))
		} else if ok {
			r.Bandwidth = v
		}
		tmpl, tErrs := parseTemplate(rel, path, asTemplate)
		mp.Errors = append(mp.Errors, tErrs...)
		r.Template = tmpl
		r.Timescale = templateTimescale(tmpl)
		if tmpl != nil {
			r.Errors = append(r.Errors, tmpl.Errors...)
		}
		if err := resolveSegments(r, p, rel.offset, path); err != nil {
			r.Errors = append(r.Errors, err)
			mp.Errors = append(mp.Errors, err)
		}
		reps = append(reps, r)
	}
	if len(reps) == 0 {
		mp.Errors = append(mp.Errors, model.NewError(model.CodeMPDInvalid, path, el.offset,
			model.StageParse, false, fmt.Sprintf("AdaptationSet %q has no Representation", contentType)))
	}
	return reps
}

func templateTimescale(t *Template) uint32 {
	if t == nil || t.Timescale == 0 {
		return 1
	}
	return t.Timescale
}

// parseTemplate merges a SegmentTemplate found on el over an inherited one.
func parseTemplate(el *rawElement, path string, inherited *Template) (*Template, []*model.Error) {
	children := childElements(el, "SegmentTemplate")
	if len(children) == 0 {
		if inherited == nil {
			return nil, nil
		}
		c := *inherited
		c.Errors = nil
		return &c, nil
	}
	t := &Template{StartNumber: 1}
	if inherited != nil {
		*t = *inherited
		t.Timeline = append([]TimelineEntry(nil), inherited.Timeline...)
		t.Errors = nil
	}
	var errs []*model.Error
	for _, c := range children {
		t.Offset = c.offset
		if v, ok, err := attrUint(c, "timescale"); err != nil {
			errs = append(errs, err.WithPath(path))
		} else if ok {
			t.Timescale = uint32(v)
			t.TimescaleOffset = attrOffset(c, "timescale")
		}
		if v, ok, err := attrUint(c, "duration"); err != nil {
			errs = append(errs, err.WithPath(path))
		} else if ok {
			t.Duration = v
		}
		if v, ok, err := attrUint(c, "startNumber"); err != nil {
			errs = append(errs, err.WithPath(path))
		} else if ok {
			t.StartNumber = v
		}
		if v, ok, err := attrUint(c, "presentationTimeOffset"); err != nil {
			errs = append(errs, err.WithPath(path))
		} else if ok {
			t.PTO = v
		}
		if s := attrString(c, "media"); s != "" {
			t.Media = s
		}
		if s := attrString(c, "initialization"); s != "" {
			t.Initialization = s
		}
		if tl := childElements(c, "SegmentTimeline"); len(tl) > 0 {
			t.HasTimeline = true
			t.Timeline = nil
			for _, s := range childElements(tl[0], "S") {
				e, eerrs := parseTimelineEntry(s, path)
				errs = append(errs, eerrs...)
				t.Timeline = append(t.Timeline, e)
			}
		}
	}
	t.Errors = errs
	return t, errs
}

func parseTimelineEntry(el *rawElement, path string) (TimelineEntry, []*model.Error) {
	e := TimelineEntry{Repeat: 0, Offset: el.offset}
	var errs []*model.Error
	if v, ok, err := attrUint(el, "t"); err != nil {
		errs = append(errs, err.WithPath(path))
	} else if ok {
		e.TimeTicks = v
	}
	if v, ok, err := attrUint(el, "d"); err != nil {
		errs = append(errs, err.WithPath(path))
	} else if ok {
		e.DurTicks = v
	}
	if v, ok, err := attrInt(el, "r"); err != nil {
		errs = append(errs, err.WithPath(path))
	} else if ok {
		e.Repeat = v
	}
	if e.DurTicks == 0 {
		errs = append(errs, model.NewError(model.CodeNegativeDuration, path, el.offset,
			model.StageParse, false, "SegmentTimeline S@d must be positive"))
	}
	return e, errs
}

// resolveSegments expands the representation's index into concrete media
// segments. A recoverable error is attached to the representation and the
// caller also records it on the MPD.
func resolveSegments(r *Representation, p Period, repOffset int64, path string) *model.Error {
	t := r.Template
	if t == nil || t.Media == "" {
		return model.NewError(model.CodeMissingIndex, path, repOffset, model.StageParse, true,
			"representation has no SegmentTemplate media index")
	}
	if t.Timescale == 0 {
		off := t.TimescaleOffset
		if off == 0 {
			off = t.Offset
		}
		return model.NewError(model.CodeZeroTimescale, path, off, model.StageParse, true,
			"representation timescale is zero")
	}
	ts := t.Timescale
	if t.HasTimeline && len(t.Timeline) == 0 {
		return model.NewError(model.CodeMissingIndex, path, t.Offset, model.StageParse, true,
			"SegmentTimeline is empty")
	}
	if !t.HasTimeline && t.Duration == 0 {
		return model.NewError(model.CodeMissingIndex, path, t.Offset, model.StageParse, true,
			"SegmentTemplate has neither duration nor SegmentTimeline")
	}
	var ticks, durs []uint64
	var offsets []int64
	if t.HasTimeline {
		cursor := uint64(0)
		for i, e := range t.Timeline {
			start := e.TimeTicks
			if start == 0 && i > 0 {
				start = cursor
			}
			repeat := e.Repeat
			if repeat < 0 {
				if p.DurNS <= 0 {
					return model.NewError(model.CodeMissingIndex, path, e.Offset, model.StageParse, true,
						"open-ended SegmentTimeline repeat requires a Period duration")
				}
				periodTicks := uint64(p.DurNS) * uint64(ts) / uint64(model.NanosPerSecond)
				if start >= periodTicks || e.DurTicks == 0 {
					repeat = 0
				} else {
					repeat = int64((periodTicks-start)/e.DurTicks) - 1
				}
			}
			if int64(len(ticks))+repeat+1 > maxSegmentsPerRepresentation {
				return model.NewError(model.CodeMPDInvalid, path, e.Offset, model.StageParse, true,
					"SegmentTimeline expands beyond the supported segment count")
			}
			for k := int64(0); k <= repeat; k++ {
				ticks = append(ticks, start+uint64(k)*e.DurTicks)
				durs = append(durs, e.DurTicks)
				offsets = append(offsets, e.Offset)
			}
			cursor = start + uint64(repeat+1)*e.DurTicks
		}
	} else {
		dur := t.Duration
		count := uint64(0)
		if p.DurNS > 0 {
			periodTicks := uint64(p.DurNS) * uint64(ts) / uint64(model.NanosPerSecond)
			if t.PTO < periodTicks {
				count = (periodTicks - t.PTO + dur - 1) / dur
			}
		}
		if count > maxSegmentsPerRepresentation {
			return model.NewError(model.CodeMPDInvalid, path, t.Offset, model.StageParse, true,
				"SegmentTemplate duration expands beyond the supported segment count")
		}
		for i := uint64(0); i < count; i++ {
			ticks = append(ticks, t.PTO+i*dur)
			durs = append(durs, dur)
			offsets = append(offsets, t.Offset)
		}
	}
	for i := range ticks {
		startNS, err := addOffsetNS(p.StartNS, ticks[i], t.PTO, ts)
		if err != nil {
			return model.NewError(model.CodeTimeOverflow, path, offsets[i], model.StageParse, true, err.Error())
		}
		durNS, err := model.TicksToNanos(durs[i], ts)
		if err != nil {
			return model.NewError(model.CodeTimeOverflow, path, offsets[i], model.StageParse, true, err.Error())
		}
		if durNS <= 0 {
			return model.NewError(model.CodeNegativeDuration, path, offsets[i], model.StageParse, true,
				fmt.Sprintf("segment %d has non-positive duration", i))
		}
		num := t.StartNumber + uint64(i)
		r.Segments = append(r.Segments, Segment{
			Number:           num,
			TimeTicks:        ticks[i],
			DurationTicks:    durs[i],
			StartNS:          startNS,
			DurationNS:       durNS,
			URI:              expandMedia(t.Media, r.ID, num, ticks[i]),
			SourceOffset:     offsets[i],
			RepresentationID: r.ID,
			Timescale:        ts,
		})
	}
	return nil
}

// addOffsetNS returns periodStart + (tick - pto) * 1e9 / timescale,
// clamped at zero for ticks before the presentation time offset.
func addOffsetNS(periodStartNS int64, tick, pto uint64, timescale uint32) (int64, error) {
	var rel uint64
	if tick >= pto {
		rel = tick - pto
	}
	ns, err := model.TicksToNanos(rel, timescale)
	if err != nil {
		return 0, err
	}
	return model.AddNanos(periodStartNS, ns)
}

func expandMedia(tmpl, repID string, number, time uint64) string {
	s := strings.ReplaceAll(tmpl, "$RepresentationID$", repID)
	s = strings.ReplaceAll(s, "$Number$", strconv.FormatUint(number, 10))
	s = expandWrapped(s, "Number", strconv.FormatUint(number, 10))
	s = expandWrapped(s, "Time", strconv.FormatUint(time, 10))
	s = expandWrapped(s, "RepresentationID", repID)
	return s
}

// expandWrapped supports the printf-style $Name%0Nd$ form.
func expandWrapped(s, name, value string) string {
	prefix := "$" + name + "%"
	for {
		start := strings.Index(s, prefix)
		if start < 0 {
			return s
		}
		end := strings.IndexByte(s[start+1:], '$')
		if end < 0 {
			return s
		}
		end += start + 1
		expr := s[start+1+len(name) : end] // e.g. %05d
		out := value
		if v, err := formatValue(value, expr); err == nil {
			out = v
		}
		s = s[:start] + out + s[end+1:]
	}
}

func formatValue(value, spec string) (string, error) {
	body := strings.TrimSuffix(strings.TrimPrefix(spec, "%"), "d")
	width, zero := 0, false
	if strings.HasPrefix(body, "0") {
		zero = true
		body = body[1:]
	}
	if body != "" {
		w, err := strconv.Atoi(body)
		if err != nil {
			return "", err
		}
		width = w
	}
	v, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return "", err
	}
	out := strconv.FormatUint(v, 10)
	pad := " "
	if zero {
		pad = "0"
	}
	for len(out) < width {
		out = pad + out
	}
	return out, nil
}
