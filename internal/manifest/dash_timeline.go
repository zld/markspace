package manifest

import (
	"fmt"
	"strconv"
	"strings"

	"archivecheck/internal/model"
)

// buildDASHPlan converts a decoded MPD into a unified plan on a shared
// timebase. Every representation is aligned to the same origin. A segment's
// presentation time is:
//
//	periodStart + (segmentMediaTime - presentationTimeOffset) / timescale
//
// converted to plan ticks exactly.
func buildDASHPlan(name string, raw *mpd, opts Options, v *Validator) *Plan {
	plan := NewPlan(FormatDASH, name)
	plan.Faults = &v.faults

	declared := declaredTimescales(raw)
	base := opts.base(0)
	if base <= 0 {
		l, err := model.LCMAll(declared)
		if err != nil || l <= 0 {
			v.fail(model.CodeManifestTimebase,
				"cannot derive a shared timebase from representation timescales %v: %v (declare a --timebase)", declared, err)
			return plan
		}
		base = l
	}
	plan.Timebase = base
	if err := opts.checkTimescales(base, declared); err != nil {
		v.faults.AddErr(err)
		return plan
	}

	var mpdDur model.Rational
	hasMPDDur := false
	if d, ok, err := parseMPDDuration(raw.MediaPresentationDuration); err != nil {
		v.fail(model.CodeManifestSyntax, "invalid @mediaPresentationDuration %q: %v", raw.MediaPresentationDuration, err)
	} else if ok {
		mpdDur, hasMPDDur = d, true
	}

	cursor := model.Time{} // running period start
	for pi := range raw.Periods {
		per := &raw.Periods[pi]
		periodID := per.ID
		if periodID == "" {
			periodID = "period-" + strconv.Itoa(pi)
		}
		if per.Start != "" {
			t, err := parseDASHTime(per.Start, base)
			if err != nil {
				v.fail(model.CodeManifestSyntax, "period %s: invalid @start %q: %v", periodID, per.Start, err)
				continue
			}
			cursor = t
		}
		periodDur, ok := periodDuration(per, cursor, mpdDur, hasMPDDur, base, periodID, v)
		if !ok {
			continue
		}
		for ai := range per.AdaptationSets {
			as := &per.AdaptationSets[ai]
			for ri := range as.Representations {
				rep, ok := buildDASHRepr(&as.Representations[ri], as, periodID, cursor, periodDur, pi, base, opts, v)
				if ok {
					plan.Representations = append(plan.Representations, rep)
				}
			}
		}
		if !periodDur.IsZero() {
			next, err := cursor.Add(periodDur)
			if err != nil {
				v.fail(model.CodeManifestTimebase, "period %s: timeline overflow", periodID)
				break
			}
			cursor = next
		}
	}
	if len(plan.Representations) == 0 && len(v.faults.All()) == 0 {
		v.fail(model.CodeManifestBoundary, "MPD declares no usable representations")
	}
	sortedReps(plan.Representations)
	if hasMPDDur {
		d, err := model.SecondsToTime(mpdDur.Num, mpdDur.Den, base)
		if err != nil {
			v.fail(model.CodeManifestTimebase, "mediaPresentationDuration does not fit timebase %d: %v", base, err)
			return plan
		}
		plan.Duration, plan.Remainder = d.Ticks, d.Rem
	} else {
		plan.Duration = maxRepEnd(plan.Representations)
	}
	return plan
}

// periodDuration resolves a period's duration from its own @duration, else
// from mediaPresentationDuration minus the period start.
func periodDuration(per *mpdPeriod, cursor model.Time, mpdDur model.Rational, hasMPDDur bool, base int64, periodID string, v *Validator) (model.Time, bool) {
	if per.Duration != "" {
		t, err := parseDASHTime(per.Duration, base)
		if err != nil {
			v.fail(model.CodeManifestSyntax, "period %s: invalid @duration %q: %v", periodID, per.Duration, err)
			return model.Time{}, false
		}
		return t, true
	}
	if !hasMPDDur {
		return model.Time{}, true
	}
	total, err := model.SecondsToTime(mpdDur.Num, mpdDur.Den, base)
	if err != nil {
		v.fail(model.CodeManifestTimebase, "period %s: mediaPresentationDuration does not fit timebase %d", periodID, base)
		return model.Time{}, false
	}
	rem, err := total.Sub(cursor)
	if err != nil || rem.Ticks < 0 {
		v.fail(model.CodeManifestBoundary, "period %s: starts after mediaPresentationDuration", periodID)
		return model.Time{}, false
	}
	return rem, true
}

func declaredTimescales(raw *mpd) []int64 {
	var out []int64
	for pi := range raw.Periods {
		for ai := range raw.Periods[pi].AdaptationSets {
			as := &raw.Periods[pi].AdaptationSets[ai]
			for ri := range as.Representations {
				ts := int64(48000)
				if tmpl := effectiveTemplate(as, &as.Representations[ri]); tmpl != nil && tmpl.Timescale > 0 {
					ts = tmpl.Timescale
				}
				out = append(out, ts)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, 48000)
	}
	return out
}

// unitConv converts counts of one representation timescale into plan ticks.
type unitConv struct {
	ts   int64
	base int64
}

func (c unitConv) ticks(units int64) (model.Time, error) {
	t, err := model.ConvertTicks(units, c.ts, c.base)
	if err != nil {
		return model.Time{}, err
	}
	if !t.Rem.IsZero() {
		return model.Time{}, fmt.Errorf("%w: %d units at %d tps maps to %s plan ticks at %d tps",
			model.ErrInexact, units, c.ts, t.Rem, c.base)
	}
	return t, nil
}

func (c unitConv) units(ticks int64) int64 {
	v, err := model.ConvertTicks(ticks, c.base, c.ts)
	if err != nil {
		return 0
	}
	return v.Ticks
}

func buildDASHRepr(repr *mpdRepr, as *mpdAdaptation, periodID string, periodStart, periodDur model.Time, pi int, base int64, opts Options, v *Validator) (Representation, bool) {
	out := Representation{ID: repr.ID, Bandwidth: repr.Bandwidth, Base: periodStart.Ticks}
	if out.ID == "" {
		v.fail(model.CodeManifestMissingField, "period %s: representation without @id", periodID)
		return out, false
	}
	tmpl := effectiveTemplate(as, repr)
	if tmpl == nil {
		v.fail(model.CodeManifestMissingField,
			"representation %s: no SegmentTemplate (only SegmentTemplate addressing is supported)", out.ID)
		return out, false
	}
	ts := tmpl.Timescale
	if ts <= 0 {
		ts = 48000
	}
	out.Timescale = ts
	if base%ts != 0 {
		v.fail(model.CodeManifestTimebase,
			"representation %s: timescale %d does not divide shared timebase %d", out.ID, ts, base)
		return out, false
	}
	conv := unitConv{ts: ts, base: base}
	media := tmpl.Media
	if media == "" {
		v.fail(model.CodeManifestMissingField, "representation %s: SegmentTemplate has no @media", out.ID)
		return out, false
	}
	if err := ValidateSimpleName(expandMedia(media, repr.ID, 1, 1)); err != nil {
		v.fail(model.CodeManifestBadRange, "representation %s: @media %q is not a local resource: %v", out.ID, media, err)
		return out, false
	}
	pto, err := conv.ticks(tmpl.PresentationTimeOffset)
	if err != nil {
		v.fail(model.CodeManifestTimebase, "representation %s: @presentationTimeOffset does not map to integral plan ticks: %v", out.ID, err)
		return out, false
	}
	startNumber := tmpl.StartNumber
	if startNumber == 0 {
		startNumber = 1
	}
	if startNumber < 0 {
		v.fail(model.CodeManifestBadRange, "representation %s: negative @startNumber %d", out.ID, startNumber)
		return out, false
	}

	b := &dashBuilder{
		rep:         &out,
		periodID:    periodID,
		periodStart: periodStart,
		periodDur:   periodDur,
		plane:       pi,
		number:      startNumber,
		media:       media,
		conv:        conv,
		pto:         pto.Ticks,
		opts:        opts,
		v:           v,
	}
	switch {
	case tmpl.Timeline != nil:
		if len(tmpl.Timeline.S) == 0 {
			v.fail(model.CodeManifestBoundary, "representation %s: empty SegmentTimeline", out.ID)
			return out, false
		}
		if !b.fromTimeline(tmpl.Timeline) {
			return out, false
		}
	case tmpl.Duration > 0:
		d, err := conv.ticks(tmpl.Duration)
		if err != nil {
			v.fail(model.CodeManifestTimebase,
				"representation %s: @duration %d at timescale %d is not an integral number of plan ticks: %v",
				out.ID, tmpl.Duration, ts, err)
			return out, false
		}
		if d.Ticks <= 0 {
			v.fail(model.CodeManifestBoundary, "representation %s: @duration maps to zero plan ticks", out.ID)
			return out, false
		}
		// A fixed @duration can also be bounded by mediaPresentationDuration,
		// which applyPeriodDuration has already folded into periodDur.
		if periodDur.IsZero() {
			v.fail(model.CodeManifestBoundary,
				"representation %s: SegmentTemplate@duration requires a period @duration, a mediaPresentationDuration, or a SegmentTimeline", out.ID)
			return out, false
		}
		if !b.fromTemplateDuration(d) {
			return out, false
		}
	default:
		v.fail(model.CodeManifestMissingField,
			"representation %s: SegmentTemplate has neither @duration nor SegmentTimeline", out.ID)
		return out, false
	}
	if len(out.Segments) == 0 {
		v.fail(model.CodeManifestBoundary, "representation %s: segment template produced no segments", out.ID)
		return out, false
	}
	return out, true
}

// dashBuilder walks one representation's media timeline.
type dashBuilder struct {
	rep         *Representation
	periodID    string
	periodStart model.Time
	periodDur   model.Time
	plane       int
	number      int64
	media       string
	conv        unitConv
	pto         int64
	cursor      model.Time // position relative to period start
	opts        Options
	v           *Validator
}

func (b *dashBuilder) add(d model.Time) bool {
	if limit := b.opts.maxSegments(); limit > 0 && len(b.rep.Segments) > limit {
		b.v.fail(model.CodeManifestBoundary,
			"representation %s: template expands beyond %d segments", b.rep.ID, limit)
		return false
	}
	if d.Ticks <= 0 {
		b.v.fail(model.CodeManifestBoundary,
			"representation %s: segment %d has a non-positive duration in plan ticks", b.rep.ID, b.number)
		return false
	}
	end, err := b.cursor.Add(d)
	if err != nil {
		b.v.fail(model.CodeManifestTimebase, "representation %s: timeline overflow", b.rep.ID)
		return false
	}
	abs, err := b.periodStart.Add(b.cursor)
	if err != nil {
		b.v.fail(model.CodeManifestTimebase, "representation %s: period timeline overflow", b.rep.ID)
		return false
	}
	mediaTime := b.conv.units(abs.Ticks - b.periodStart.Ticks + b.pto)
	b.rep.Segments = append(b.rep.Segments, Segment{
		Index:          len(b.rep.Segments),
		ID:             strconv.FormatInt(b.number, 10),
		URI:            expandMedia(b.media, b.rep.ID, b.number, mediaTime),
		Start:          abs.Ticks,
		Duration:       d.Ticks,
		Remainder:      b.cursor.Rem,
		EndRemainder:   end.Rem,
		Period:         b.periodID,
		Representation: b.rep.ID,
		Discontinuity:  b.plane > 0 && len(b.rep.Segments) == 0,
	})
	b.cursor = end
	b.number++
	return true
}

func (b *dashBuilder) fromTemplateDuration(d model.Time) bool {
	for {
		end, err := b.cursor.Add(d)
		if err != nil {
			b.v.fail(model.CodeManifestTimebase, "representation %s: timeline overflow", b.rep.ID)
			return false
		}
		if end.Cmp(b.periodDur) > 0 {
			return true
		}
		if !b.add(d) {
			return false
		}
		if end.Cmp(b.periodDur) == 0 {
			return true
		}
	}
}

func (b *dashBuilder) fromTimeline(tl *mpdSegmentTimeline) bool {
	for si := range tl.S {
		s := &tl.S[si]
		if s.D <= 0 {
			b.v.fail(model.CodeManifestBoundary,
				"representation %s: SegmentTimeline S[%d] has non-positive @d=%d", b.rep.ID, si, s.D)
			return false
		}
		d, err := b.conv.ticks(s.D)
		if err != nil {
			b.v.fail(model.CodeManifestTimebase,
				"representation %s: SegmentTimeline S[%d] @d=%d is not an integral number of plan ticks: %v",
				b.rep.ID, si, s.D, err)
			return false
		}
		if s.T != "" {
			tu, err := strconv.ParseInt(strings.TrimSpace(s.T), 10, 64)
			if err != nil || tu < 0 {
				b.v.fail(model.CodeManifestSyntax,
					"representation %s: SegmentTimeline S[%d] has invalid @t=%q", b.rep.ID, si, s.T)
				return false
			}
			t, err := b.conv.ticks(tu)
			if err != nil {
				b.v.fail(model.CodeManifestTimebase,
					"representation %s: SegmentTimeline S[%d] @t=%d is not an integral number of plan ticks: %v",
					b.rep.ID, si, tu, err)
				return false
			}
			rel := model.Time{Ticks: t.Ticks - b.pto}
			if rel.Ticks < 0 {
				b.v.fail(model.CodeManifestBoundary,
					"representation %s: SegmentTimeline S[%d] @t=%d precedes presentationTimeOffset", b.rep.ID, si, tu)
				return false
			}
			b.cursor = rel
		}
		if !s.RSet {
			if !b.add(d) {
				return false
			}
			continue
		}
		if s.R < 0 {
			if !b.repeatToEnd(d, tl, si) {
				return false
			}
			continue
		}
		for i := int64(0); i <= s.R; i++ {
			if !b.add(d) {
				return false
			}
		}
	}
	return true
}

// repeatToEnd implements r="-1": repeat until the next S@t or the period end.
func (b *dashBuilder) repeatToEnd(d model.Time, tl *mpdSegmentTimeline, si int) bool {
	if b.periodDur.IsZero() {
		b.v.fail(model.CodeManifestBoundary,
			"representation %s: SegmentTimeline S[%d] uses r=-1 but the period has no duration", b.rep.ID, si)
		return false
	}
	var stop *model.Time
	if si+1 < len(tl.S) && tl.S[si+1].T != "" {
		tu, err := strconv.ParseInt(strings.TrimSpace(tl.S[si+1].T), 10, 64)
		if err != nil || tu < 0 {
			b.v.fail(model.CodeManifestSyntax,
				"representation %s: SegmentTimeline S[%d] has invalid @t=%q", b.rep.ID, si+1, tl.S[si+1].T)
			return false
		}
		t, err := b.conv.ticks(tu)
		if err != nil {
			b.v.fail(model.CodeManifestTimebase,
				"representation %s: SegmentTimeline S[%d] @t=%d is not integral in plan ticks: %v", b.rep.ID, si+1, tu, err)
			return false
		}
		v := model.Time{Ticks: t.Ticks - b.pto}
		stop = &v
	}
	for {
		end, err := b.cursor.Add(d)
		if err != nil {
			b.v.fail(model.CodeManifestTimebase, "representation %s: timeline overflow", b.rep.ID)
			return false
		}
		if stop != nil && end.Cmp(*stop) >= 0 {
			return true
		}
		if stop == nil && end.Cmp(b.periodDur) > 0 {
			return true
		}
		if !b.add(d) {
			return false
		}
		if limit := b.opts.maxSegments(); limit > 0 && len(b.rep.Segments) > limit {
			b.v.fail(model.CodeManifestBoundary,
				"representation %s: SegmentTimeline S[%d] with r=-1 exceeds %d segments", b.rep.ID, si, limit)
			return false
		}
	}
}

// effectiveTemplate merges the representation template over the adaptation-set
// template. Absent attributes fall through, as DASH requires.
func effectiveTemplate(as *mpdAdaptation, repr *mpdRepr) *mpdSegmentTmpl {
	if repr.SegmentTemplate == nil {
		return as.SegmentTemplate
	}
	if as.SegmentTemplate == nil {
		return repr.SegmentTemplate
	}
	merged := *as.SegmentTemplate
	over := repr.SegmentTemplate
	if over.Timescale > 0 {
		merged.Timescale = over.Timescale
	}
	if over.Duration > 0 {
		merged.Duration = over.Duration
	}
	if over.StartNumber > 0 {
		merged.StartNumber = over.StartNumber
	}
	if over.PresentationTimeOffset > 0 {
		merged.PresentationTimeOffset = over.PresentationTimeOffset
	}
	if over.Media != "" {
		merged.Media = over.Media
	}
	if over.Initialization != "" {
		merged.Initialization = over.Initialization
	}
	if over.Timeline != nil {
		merged.Timeline = over.Timeline
	}
	return &merged
}

func parseDASHTime(text string, base int64) (model.Time, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return model.Time{}, fmt.Errorf("empty time")
	}
	if strings.HasPrefix(s, "P") {
		r, ok, err := parseMPDDuration(s)
		if err != nil || !ok {
			return model.Time{}, errBadDuration(s)
		}
		return model.SecondsToTime(r.Num, r.Den, base)
	}
	if strings.Contains(s, ":") {
		parts := strings.Split(s, ":")
		if len(parts) != 3 {
			return model.Time{}, errBadDuration(s)
		}
		total := model.Rational{Den: 1}
		mult := []int64{3600, 60, 1}
		for i, p := range parts {
			r, err := ParseDecimalSeconds(p)
			if err != nil {
				return model.Time{}, errBadDuration(s)
			}
			r, err = mulRationalR(r, mult[i])
			if err != nil {
				return model.Time{}, err
			}
			total, err = total.Add(r)
			if err != nil {
				return model.Time{}, err
			}
		}
		return model.SecondsToTime(total.Num, total.Den, base)
	}
	r, err := ParseDecimalSeconds(s)
	if err != nil {
		return model.Time{}, errBadDuration(s)
	}
	return model.SecondsToTime(r.Num, r.Den, base)
}

// expandMedia performs the DASH $Identifier$ substitutions the kernel needs.
func expandMedia(tmpl, representationID string, number, timeUnits int64) string {
	out := strings.ReplaceAll(tmpl, "$$", "\x00DOLLAR\x00")
	out = strings.ReplaceAll(out, "$RepresentationID$", representationID)
	out = strings.ReplaceAll(out, "$Number$", strconv.FormatInt(number, 10))
	out = strings.ReplaceAll(out, "$Time$", strconv.FormatInt(timeUnits, 10))
	return strings.ReplaceAll(out, "\x00DOLLAR\x00", "$")
}

func maxRepEnd(reps []Representation) int64 {
	var maxEnd int64
	for _, r := range reps {
		for _, s := range r.Segments {
			if e := s.End(); e > maxEnd {
				maxEnd = e
			}
		}
	}
	return maxEnd
}
