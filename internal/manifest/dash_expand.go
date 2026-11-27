package manifest

import (
	"fmt"
	"strconv"
	"strings"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

// expandDASH converts the tokenized MPD into normalized segments.
func expandDASH(m *Manifest, doc *dashDoc, data []byte) {
	// Resolve each period's presentation start from its own @start or from the
	// previous period's end, then its end from @duration, the next period's
	// @start, or the MPD-level mediaPresentationDuration.
	mpdEnd := clock.Zero
	m.presentationDuration = doc.duration
	if doc.duration != "" {
		if d, err := clock.ParseISODuration(doc.duration); err == nil {
			mpdEnd = d
		} else {
			m.Issues = append(m.Issues, issueAt(m.File, 1, 0, fault.CodeParse,
				fmt.Sprintf("mediaPresentationDuration %q: %v", doc.duration, err)))
		}
	}
	periodStart := clock.Zero
	for pi, p := range doc.periods {
		name := p.id
		if name == "" {
			name = fmt.Sprintf("period-%d", pi)
		}
		start := periodStart
		if p.start != "" {
			if t, err := clock.ParseISODuration(p.start); err == nil {
				start = t
			} else {
				m.Issues = append(m.Issues, issueAt(m.File, p.line, int64(p.offset), fault.CodeParse,
					fmt.Sprintf("period %s start %q: %v", name, p.start, err)))
			}
		}
		periodEnd := start
		endKnown := false
		if p.duration != "" {
			if d, err := clock.ParseISODuration(p.duration); err == nil {
				periodEnd = start.Add(d)
				endKnown = true
			} else {
				m.Issues = append(m.Issues, issueAt(m.File, p.line, int64(p.offset), fault.CodeParse,
					fmt.Sprintf("period %s duration %q: %v", name, p.duration, err)))
			}
		}
		if !endKnown && pi+1 < len(doc.periods) && doc.periods[pi+1].start != "" {
			if t, err := clock.ParseISODuration(doc.periods[pi+1].start); err == nil && t.Cmp(start) >= 0 {
				periodEnd = t
				endKnown = true
			}
		}
		if !endKnown && mpdEnd.Cmp(start) > 0 {
			periodEnd = mpdEnd
			endKnown = true
		}
		if !endKnown {
			m.Issues = append(m.Issues, issueAt(m.File, p.line, int64(p.offset), fault.CodeDashNoDuration,
				fmt.Sprintf("period %s has no determinable duration", name)))
		}
		for _, set := range p.sets {
			for _, rep := range set.reps {
				expandRepresentation(m, name, start, periodEnd, p, set, rep)
			}
		}
		if endKnown {
			periodStart = periodEnd
		} else {
			periodStart = start
		}
	}
}

func expandRepresentation(m *Manifest, period string, start, periodEnd clock.Time, p *dashPeriod, set *dashAdaptation, rep *dashRep) {
	if rep.id == "" {
		rep.id = fmt.Sprintf("rep-%d", len(m.Segments))
	}
	switch {
	case rep.tmpl != nil && rep.tmpl.media != "":
		expandTemplate(m, period, start, periodEnd, p, set, rep)
	case rep.list != nil && len(rep.list.urls) > 0:
		expandList(m, period, start, rep)
	case rep.baseURL != "":
		expandSingle(m, period, start, periodEnd, p, set, rep)
	default:
		m.Issues = append(m.Issues, fault.Issue{
			Code: fault.CodeDashBadRef, Severity: fault.SeverityError, Action: fault.ActionIsolated,
			Message: "representation has no segment reference",
			Source: fault.Source{
				Manifest: m.File, Representation: rep.id,
				Loc:   fault.Locator{File: m.File, Line: rep.line, Offset: int64(rep.offset)},
				Start: start, End: start,
			},
		})
	}
}

func expandTemplate(m *Manifest, period string, start, periodEnd clock.Time, p *dashPeriod, set *dashAdaptation, rep *dashRep) {
	tmpl := rep.tmpl
	loc := fault.Locator{File: m.File, Line: tmpl.line, Offset: int64(tmpl.offset)}
	startNumber := 1
	if tmpl.startNumber != "" {
		n, err := strconv.Atoi(tmpl.startNumber)
		if err != nil || n < 0 {
			m.Issues = append(m.Issues, refIssue(m.File, rep.id, "", loc, start,
				fault.CodeParse, fmt.Sprintf("invalid startNumber %q", tmpl.startNumber)))
			return
		}
		startNumber = n
	}
	cur := start
	if len(tmpl.timeline) > 0 {
		number := startNumber
		for _, s := range tmpl.timeline {
			sloc := fault.Locator{File: m.File, Line: s.line, Offset: s.offset}
			if s.hasT {
				t, err := ticks(s.t, tmpl.scale())
				if err != nil {
					m.Issues = append(m.Issues, refIssue(m.File, rep.id, "", sloc, cur,
						fault.CodeParse, fmt.Sprintf("bad S@t %q: %v", s.t, err)))
					continue
				}
				cur = t
			}
			repeat := 1
			if s.r != "" {
				r, err := strconv.Atoi(s.r)
				if err != nil || r < 0 {
					m.Issues = append(m.Issues, refIssue(m.File, rep.id, "", sloc, cur,
						fault.CodeParse, fmt.Sprintf("bad S@r %q", s.r)))
					continue
				}
				repeat += r
			}
			d, err := ticks(s.d, tmpl.scale())
			if err != nil || d.Num <= 0 {
				m.Issues = append(m.Issues, refIssue(m.File, rep.id, "", sloc, cur,
					fault.CodeDashNoDuration, "SegmentTimeline entry without a positive @d"))
				m.Segments = append(m.Segments, Segment{
					Representation: rep.id, URL: expandMedia(tmpl.media, rep.id, number),
					Start: cur, End: cur, Index: number, Declared: false, Isolated: true,
					Line: s.line, Offset: s.offset, Period: period,
				})
				number++
				continue
			}
			for k := 0; k < repeat; k++ {
				url := expandMedia(tmpl.media, rep.id, number)
				seg := Segment{
					Representation: rep.id, URL: url, Start: cur, End: cur.Add(d),
					Duration: d, Declared: true, Index: number,
					Line: tmpl.line, Offset: int64(tmpl.offset), Period: period,
					Discontinuity: k == 0 && number == startNumber && start.Cmp(clock.Zero) > 0,
				}
				m.Segments = append(m.Segments, seg)
				cur = seg.End
				number++
			}
		}
		return
	}
	durStr := tmpl.duration
	d, err := ticks(durStr, tmpl.scale())
	if err != nil || d.Num <= 0 {
		msg := "SegmentTemplate without @duration or SegmentTimeline"
		if durStr != "" {
			msg = fmt.Sprintf("invalid SegmentTemplate @duration %q", durStr)
		}
		m.Issues = append(m.Issues, refIssue(m.File, rep.id, "", loc, cur,
			fault.CodeDashNoDuration, msg))
		m.Segments = append(m.Segments, Segment{
			Representation: rep.id, URL: expandMedia(tmpl.media, rep.id, startNumber),
			Start: cur, End: cur, Index: startNumber, Declared: false, Isolated: true,
			Line: tmpl.line, Offset: int64(tmpl.offset), Period: period,
		})
		return
	}
	total := periodEnd.Sub(start)
	count := 0
	if total.Num > 0 && total.Den > 0 {
		// exact ceil(total/d)
		num := total.Num * d.Den
		den := total.Den * d.Num
		count = int((num + den - 1) / den)
	}
	for i := 0; i < count; i++ {
		number := startNumber + i
		url := expandMedia(tmpl.media, rep.id, number)
		seg := Segment{
			Representation: rep.id, URL: url, Start: cur, End: cur.Add(d),
			Duration: d, Declared: true, Index: number,
			Line: tmpl.line, Offset: int64(tmpl.offset), Period: period,
			Discontinuity: i == 0 && start.Cmp(clock.Zero) > 0,
		}
		m.Segments = append(m.Segments, seg)
		cur = seg.End
	}
}

func (r *dashRep) listScale() uint32 { return r.list.scale() }

func expandList(m *Manifest, period string, start clock.Time, rep *dashRep) {
	cur := start
	for i, u := range rep.list.urls {
		loc := fault.Locator{File: m.File, Line: u.line, Offset: int64(u.offset)}
		durStr := u.duration
		if durStr == "" {
			durStr = rep.list.duration
		}
		d, err := ticks(durStr, rep.listScale())
		if err != nil || d.Num <= 0 {
			m.Issues = append(m.Issues, refIssue(m.File, rep.id, u.media, loc, cur,
				fault.CodeDashNoDuration, "SegmentURL without a usable duration"))
			m.Segments = append(m.Segments, Segment{
				Representation: rep.id, URL: u.media, Start: cur, End: cur,
				Index: i, Declared: false, Isolated: true,
				Line: u.line, Offset: int64(u.offset), Period: period,
			})
			continue
		}
		m.Segments = append(m.Segments, Segment{
			Representation: rep.id, URL: u.media, Start: cur, End: cur.Add(d),
			Duration: d, Declared: true, Index: i,
			Line: u.line, Offset: int64(u.offset), Period: period,
			Discontinuity: i == 0 && start.Cmp(clock.Zero) > 0,
		})
		cur = cur.Add(d)
	}
}

func expandSingle(m *Manifest, period string, start, periodEnd clock.Time, p *dashPeriod, set *dashAdaptation, rep *dashRep) {
	durStr := p.duration
	if durStr == "" {
		durStr = set.duration
	}
	if durStr == "" {
		durStr = m.presentationDuration
	}
	d, err := clock.ParseISODuration(durStr)
	if err != nil || d.Num <= 0 {
		m.Issues = append(m.Issues, refIssue(m.File, rep.id, rep.baseURL,
			fault.Locator{File: m.File, Line: rep.line, Offset: int64(rep.offset)}, start,
			fault.CodeDashNoDuration, "BaseURL segment without a usable duration"))
		m.Segments = append(m.Segments, Segment{
			Representation: rep.id, URL: rep.baseURL, Start: start, End: start,
			Index: 0, Declared: false, Isolated: true,
			Line: rep.line, Offset: int64(rep.offset), Period: period,
		})
		return
	}
	m.Segments = append(m.Segments, Segment{
		Representation: rep.id, URL: rep.baseURL, Start: start, End: start.Add(d),
		Duration: d, Declared: true, Index: 0,
		Line: rep.line, Offset: int64(rep.offset), Period: period,
		Discontinuity: start.Cmp(clock.Zero) > 0,
	})
}

// ticks converts a DASH tick count at a timescale into exact seconds. DASH
// SegmentTemplate@duration, SegmentTimeline/@t, /@d and SegmentList@duration
// are integer ticks in the template's timescale, not ISO 8601 strings.
func ticks(value string, timescale uint32) (clock.Time, error) {
	if timescale == 0 {
		timescale = 1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return clock.Time{}, fmt.Errorf("bad tick value %q: %w", value, err)
	}
	return clock.FromTimescale(n, timescale)
}

// timelineScale returns the effective timescale of a template.
func (t *dashTemplate) scale() uint32 {
	if t.timescale == "" {
		return 1
	}
	n, err := strconv.ParseUint(t.timescale, 10, 32)
	if err != nil || n == 0 {
		return 1
	}
	return uint32(n)
}

// expandMedia substitutes the standard DASH template identifiers.
func expandMedia(media, repID string, number int) string {
	out := strings.ReplaceAll(media, "$RepresentationID$", repID)
	out = strings.ReplaceAll(out, "$Number$", strconv.Itoa(number))
	out = strings.ReplaceAll(out, "$Number%05d$", fmt.Sprintf("%05d", number))
	return out
}

func refIssue(file, rep, seg string, loc fault.Locator, at clock.Time, code fault.Code, msg string) fault.Issue {
	return fault.Issue{
		Code: code, Severity: fault.SeverityError, Action: fault.ActionIsolated,
		Message: msg,
		Source: fault.Source{
			Manifest: file, Representation: rep, Segment: seg,
			Loc: loc, Start: at, End: at,
		},
	}
}
