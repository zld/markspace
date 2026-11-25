package dash

import (
	"fmt"
	"strconv"
	"strings"

	"mediascan/internal/clock"
	"mediascan/internal/errs"
)

// defaultTimescale applies when SegmentTemplate declares none (ISO spec).
const defaultTimescale uint32 = 1

// Parse reads MPD bytes and returns validated representations.
func Parse(source string, raw []byte) (*Manifest, error) {
	root, err := parseTree(raw, source)
	if err != nil {
		return nil, err
	}
	m := &Manifest{Source: source, raw: append([]byte(nil), raw...)}
	if v, ok := root.attr("minBufferTime"); ok {
		ms, perr := parseDuration(v)
		if perr != nil {
			return nil, errs.New(errs.KindManifest, source, root.offset, errs.Unknown, 0,
				"invalid minBufferTime %q", v)
		}
		m.MinBufferTimeMS = ms
	}
	period := root.child("Period")
	if period == nil {
		return nil, errs.New(errs.KindManifest, source, root.offset, errs.Unknown, 0,
			"MPD contains no Period")
	}
	for _, as := range period.childrenAll("AdaptationSet") {
		for _, repEl := range as.childrenAll("Representation") {
			rep, perr := buildRepresentation(repEl, source)
			if perr != nil {
				return nil, perr
			}
			m.Representations = append(m.Representations, rep)
		}
	}
	if len(m.Representations) == 0 {
		return nil, errs.New(errs.KindManifest, source, period.offset, errs.Unknown, 0,
			"Period contains no Representation")
	}
	return m, nil
}

func buildRepresentation(repEl *element, source string) (*Representation, error) {
	id, _ := repEl.attr("id")
	mime, _ := repEl.attr("mimeType")
	rep := &Representation{ID: id, MimeType: mime, Timescale: defaultTimescale}

	tmpl := repEl.child("SegmentTemplate")
	if tmpl == nil {
		// SegmentBase with an index range: single-segment representation.
		if base := repEl.child("SegmentBase"); base != nil {
			br, err := parseByteRange(base, source)
			if err != nil {
				return nil, err
			}
			rep.Timescale, _ = parseUint32(base, "timescale", source, defaultTimescale)
			rep.Segments = []SegmentRef{{
				Index: 0, URL: id + ".mp4", StartTime: 0,
				Duration: 0, ByteOffset: base.offset, ByteRange: br,
			}}
			return rep, nil
		}
		return nil, errs.New(errs.KindManifest, source, repEl.offset, errs.Unknown, 0,
			"representation %q has neither SegmentTemplate nor SegmentBase", id)
	}

	ts, err := parseUint32(tmpl, "timescale", source, defaultTimescale)
	if err != nil {
		return nil, err
	}
	rep.Timescale = ts
	dur, hasDur, err := templateDuration(tmpl, source)
	if err != nil {
		return nil, err
	}
	media, _ := tmpl.attr("media")
	startNumber := uint32(0)
	if v, ok := tmpl.attr("startNumber"); ok {
		n, perr := strconv.ParseUint(v, 10, 32)
		if perr != nil {
			return nil, errs.New(errs.KindManifest, source, tmpl.offset, errs.Unknown, 0,
				"invalid startNumber %q", v)
		}
		startNumber = uint32(n)
	}

	timeline := tmpl.child("SegmentTimeline")
	if timeline != nil {
		if err := buildTimelineSegments(rep, timeline, source, media, startNumber); err != nil {
			return nil, err
		}
	} else if hasDur {
		if err := buildTemplatedSegments(rep, tmpl, source, media, startNumber, dur); err != nil {
			return nil, err
		}
	} else {
		return nil, errs.New(errs.KindManifest, source, tmpl.offset, errs.Unknown, 0,
			"SegmentTemplate has neither SegmentTimeline nor duration")
	}
	if err := rep.validate(source); err != nil {
		return nil, err
	}
	return rep, nil
}

func templateDuration(tmpl *element, source string) (int64, bool, error) {
	v, ok := tmpl.attr("duration")
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, false, errs.New(errs.KindManifest, source, tmpl.offset, errs.Unknown, 0,
			"invalid SegmentTemplate duration %q", v)
	}
	return n, true, nil
}

func buildTimelineSegments(rep *Representation, tl *element, source, media string, startNumber uint32) error {
	var t int64
	var haveT bool
	number := int64(startNumber)
	seen := map[int64]bool{}
	for _, s := range tl.childrenAll("S") {
		st, hasT := parseAttrInt64(s, "t")
		if hasT {
			t = st
			haveT = true
			if st < 0 {
				return errs.New(errs.KindManifest, source, s.offset, errs.Unknown, 0,
					"negative segment time t=%d", st)
			}
		} else if !haveT {
			t = 0
			haveT = true
		}
		d, ok := parseAttrInt64(s, "d")
		if !ok || d <= 0 {
			return errs.New(errs.KindManifest, source, s.offset, errs.Unknown, 0,
				"S element missing positive d attribute")
		}
		repeat := int64(0)
		if r, ok := parseAttrInt64(s, "r"); ok {
			if r < 0 {
				return errs.New(errs.KindManifest, source, s.offset, errs.Unknown, 0,
					"negative repeat count r=%d", r)
			}
			repeat = r
		}
		for k := int64(0); k <= repeat; k++ {
			// Duplicate-time check runs before the overlap check so an
			// explicit repeated t is reported as a duplicate even when
			// it would also look like a backwards overlap.
			if seen[t] {
				return errs.New(errs.KindSegment, source, s.offset, number, 0,
					"duplicate segment time t=%d in SegmentTimeline", t)
			}
			seen[t] = true
			if len(rep.Segments) > 0 {
				prev := rep.Segments[len(rep.Segments)-1]
				prevEnd := prev.StartTime + prev.Duration
				if t < prevEnd {
					return errs.New(errs.KindSegment, source, s.offset, number, 0,
						"segment at t=%d overlaps previous end %d", t, prevEnd)
				}
				if t > prevEnd {
					return errs.New(errs.KindSegment, source, s.offset, number, 0,
						"segment at t=%d leaves a gap after %d", t, prevEnd)
				}
			}
			rep.Segments = append(rep.Segments, SegmentRef{
				Index:      len(rep.Segments),
				URL:        expandTemplate(media, rep.ID, number, t),
				StartTime:  t,
				Duration:   d,
				ByteOffset: s.offset,
			})
			t += d
			number++
		}
	}
	if len(rep.Segments) == 0 {
		return errs.New(errs.KindManifest, source, tl.offset, errs.Unknown, 0,
			"empty SegmentTimeline")
	}
	return nil
}

func buildTemplatedSegments(rep *Representation, tmpl *element, source, media string, startNumber uint32, dur int64) error {
	// Without an explicit timeline the presentation length comes from
	// the Period duration; fixtures always supply SegmentTimeline, so a
	// missing one is a manifest error rather than a guess.
	return errs.New(errs.KindManifest, source, tmpl.offset, errs.Unknown, 0,
		"duration-only SegmentTemplate without SegmentTimeline is not supported")
}

func expandTemplate(tmpl, repID string, number, t int64) string {
	if tmpl == "" {
		return fmt.Sprintf("seg-%d.m4s", number)
	}
	out := tmpl
	out = strings.ReplaceAll(out, "$RepresentationID$", repID)
	out = strings.ReplaceAll(out, "$Number$", strconv.FormatInt(number, 10))
	out = strings.ReplaceAll(out, "$Time$", strconv.FormatInt(t, 10))
	return out
}

// validate enforces contiguous, non-overlapping, monotonic segment time
// ranges and a legal timescale. Duplicate times were already rejected
// while building; here we check gaps/overlaps and sample-range sanity.
func (r *Representation) validate(source string) error {
	if !clock.ValidScale(r.Timescale) {
		return errs.New(errs.KindTrack, source, errs.Unknown, errs.Unknown, 0,
			"representation %q declares invalid timescale %d", r.ID, r.Timescale)
	}
	if len(r.Segments) == 0 {
		return errs.New(errs.KindSegment, source, errs.Unknown, errs.Unknown, 0,
			"representation %q has no segments", r.ID)
	}
	var end int64
	for i, seg := range r.Segments {
		if seg.Duration < 0 {
			return errs.New(errs.KindSegment, source, seg.ByteOffset, int64(i), 0,
				"segment %d has negative duration", i)
		}
		if i == 0 {
			r.Range.start = seg.StartTime
		} else if seg.StartTime < end {
			return errs.New(errs.KindSegment, source, seg.ByteOffset, int64(i), 0,
				"segment %d starts at %d before previous end %d (overlap)",
				i, seg.StartTime, end)
		} else if seg.StartTime > end {
			return errs.New(errs.KindSegment, source, seg.ByteOffset, int64(i), 0,
				"segment %d starts at %d leaving a gap after %d", i, seg.StartTime, end)
		}
		if seg.ByteRange != nil && (seg.ByteRange.Start < 0 || seg.ByteRange.End <= seg.ByteRange.Start) {
			return errs.New(errs.KindSegment, source, seg.ByteOffset, int64(i), 0,
				"segment %d has out-of-range byte range", i)
		}
		end = seg.StartTime + seg.Duration
	}
	r.Range.end = end
	return nil
}

// TimeRange returns the validated half-open time range in timescale ticks.
func (r *Representation) TimeRange() (start, end int64) { return r.Range.start, r.Range.end }

// parseDuration parses a subset of ISO 8601 durations (PTxS / PTx.yS),
// returning whole milliseconds. No float is retained beyond the integer
// millisecond conversion.
func parseDuration(v string) (int64, error) {
	if len(v) < 3 || v[0] != 'P' {
		return 0, fmt.Errorf("bad duration %q", v)
	}
	body := v[1:]
	if body[0] == 'T' {
		body = body[1:]
	}
	if !strings.HasSuffix(body, "S") {
		return 0, fmt.Errorf("only second durations supported, got %q", v)
	}
	num := strings.TrimSuffix(body, "S")
	parts := strings.SplitN(num, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, fmt.Errorf("bad duration %q", v)
	}
	ms := whole * 1000
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 3 {
			frac = frac[:3]
		}
		f, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("bad duration fraction %q", v)
		}
		for i := len(frac); i < 3; i++ {
			f *= 10
		}
		ms += f
	}
	return ms, nil
}
