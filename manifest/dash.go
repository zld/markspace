package manifest

import (
	"encoding/xml"
	"fmt"
	"strings"

	"mediamux/media"
)

// dashMPD mirrors the subset of the DASH schema needed for segment timing.
type dashMPD struct {
	XMLName                   xml.Name     `xml:"MPD"`
	Type                      string       `xml:"type,attr"`
	MediaPresentationDuration string       `xml:"mediaPresentationDuration,attr"`
	BaseURL                   string       `xml:"BaseURL"`
	Periods                   []dashPeriod `xml:"Period"`
	Duration                  string       `xml:"duration,attr"`
}

type dashPeriod struct {
	ID             string      `xml:"id,attr"`
	Start          string      `xml:"start,attr"`
	Duration       string      `xml:"duration,attr"`
	AdaptationSets []dashAdapt `xml:"AdaptationSet"`
}

type dashAdapt struct {
	ContentType     string               `xml:"contentType,attr"`
	MimeType        string               `xml:"mimeType,attr"`
	SegmentTemplate *dashTemplate        `xml:"SegmentTemplate"`
	Representations []dashRepresentation `xml:"Representation"`
}

type dashRepresentation struct {
	ID              string        `xml:"id,attr"`
	Bandwidth       int64         `xml:"bandwidth,attr"`
	Timescale       int64         `xml:"timescale,attr"`
	Codecs          string        `xml:"codecs,attr"`
	SegmentTemplate *dashTemplate `xml:"SegmentTemplate"`
	BaseURL         string        `xml:"BaseURL"`
}

type dashTemplate struct {
	Timescale      int64         `xml:"timescale,attr"`
	Duration       int64         `xml:"duration,attr"`
	StartNumber    int64         `xml:"startNumber,attr"`
	Media          string        `xml:"media,attr"`
	Initialization string        `xml:"initialization,attr"`
	Timeline       *dashTimeline `xml:"SegmentTimeline"`
}

type dashTimeline struct {
	S []dashS `xml:"S"`
}

type dashS struct {
	T int64 `xml:"t,attr"`
	D int64 `xml:"d,attr"`
	R int64 `xml:"r,attr"`
}

// ParseDASH parses a DASH MPD and flattens its first usable representation
// into the unified timeline. The representation timescale is honoured exactly:
// each segment duration becomes an exact rational and its sub-tick remainder
// is folded into the document's alignment report.
func ParseDASH(src string) (*Document, error) {
	if strings.TrimSpace(src) == "" {
		return nil, ErrLine(media.CodeInput, 0, "empty DASH manifest")
	}
	var mpd dashMPD
	dec := xml.NewDecoder(strings.NewReader(src))
	if err := dec.Decode(&mpd); err != nil {
		off := lineOffset(src, err)
		return nil, ErrLine(media.CodeManifestDrift, off, "invalid DASH XML: "+err.Error())
	}
	if mpd.XMLName.Local != "MPD" {
		return nil, ErrLine(media.CodeInput, 0, "DASH document is not an MPD")
	}
	rep, tmpl, period, err := selectDASHRepresentation(&mpd)
	if err != nil {
		return nil, err
	}
	ts := rep.Timescale
	if ts <= 0 {
		ts = 1000
	}
	if tmpl.Timescale > 0 {
		ts = tmpl.Timescale
	}
	b := newBuilder(KindDASH, tmpl.StartNumber)
	periodStart := media.Ratio{}
	if period.Start != "" {
		p, err := parseISODuration(period.Start)
		if err != nil {
			return nil, ErrLine(media.CodeManifestDrift, 0, "invalid period start")
		}
		periodStart = p
	}

	durations, err := dashSegmentDurations(&mpd, tmpl, ts)
	if err != nil {
		return nil, err
	}
	disc := false
	for _, d := range durations {
		declared := media.NewRatio(d.duration, ts)
		if err := b.add(fmt.Sprintf("%s-%d", repID(rep), len(b.segments)),
			tmpl.Initialization, declared, disc, d.start, media.Timescale(ts)); err != nil {
			return nil, err
		}
		disc = false
	}
	if len(b.segments) == 0 {
		return nil, ErrLine(media.CodeInput, 0, "DASH representation has no segments")
	}
	// Shift a non-zero period start onto the same absolute timeline without
	// disturbing the exact accumulator.
	if !periodStart.IsZero() {
		offTicks, _, err := floorTicks(periodStart, OutputTimescale)
		if err != nil {
			return nil, ErrLine(media.CodeManifestDrift, 0, "period start overflow")
		}
		for i := range b.segments {
			b.segments[i].Start = media.NewTime(b.segments[i].Start.Value+offTicks, OutputTimescale)
		}
	}
	target := media.NewRatio(tmpl.Duration, ts)
	if tmpl.Duration <= 0 {
		// A pure SegmentTimeline has no template duration; the largest
		// declared segment stands in as the target.
		for _, seg := range b.segments {
			if seg.Declared.Cmp(target) > 0 {
				target = seg.Declared
			}
		}
	}
	doc := b.finish(target, rep.Bandwidth)
	doc.MediaSequence = tmpl.StartNumber
	return doc, nil
}

type dashDuration struct {
	start    int64
	duration int64
}

// dashSegmentDurations expands either a SegmentTimeline or a fixed-duration
// template into explicit per-segment source ticks.
func dashSegmentDurations(mpd *dashMPD, tmpl *dashTemplate, ts int64) ([]dashDuration, error) {
	if tmpl.Timeline != nil && len(tmpl.Timeline.S) > 0 {
		var out []dashDuration
		var cursor int64
		for _, s := range tmpl.Timeline.S {
			start := s.T
			if start == 0 && cursor != 0 {
				start = cursor
			}
			if s.D <= 0 {
				return nil, ErrLine(media.CodeManifestDrift, 0, "SegmentTimeline entry has non-positive duration")
			}
			if s.R < 0 {
				return nil, ErrLine(media.CodeManifestDrift, 0, "unbounded SegmentTimeline repeat is not supported")
			}
			n := s.R + 1
			if n > 100000 || len(out)+int(n) > 100000 {
				return nil, ErrLine(media.CodeManifestDrift, 0, "SegmentTimeline repeat too large")
			}
			for i := int64(0); i < n; i++ {
				out = append(out, dashDuration{start: start + i*s.D, duration: s.D})
			}
			cursor = start + n*s.D
		}
		return out, nil
	}
	if tmpl.Duration <= 0 {
		return nil, ErrLine(media.CodeManifestDrift, 0, "SegmentTemplate has neither duration nor SegmentTimeline")
	}
	if mpd.MediaPresentationDuration == "" {
		return nil, ErrLine(media.CodeManifestDrift, 0, "fixed template duration requires mediaPresentationDuration")
	}
	pd, err := parseISODuration(mpd.MediaPresentationDuration)
	if err != nil {
		return nil, ErrLine(media.CodeManifestDrift, 0, "invalid mediaPresentationDuration")
	}
	total, _, err := floorTicks(pd, media.Timescale(ts))
	if err != nil || total <= 0 {
		return nil, ErrLine(media.CodeManifestDrift, 0, "mediaPresentationDuration is unusable")
	}
	want := total / tmpl.Duration
	if total%tmpl.Duration != 0 {
		want++
	}
	if want > 100000 {
		return nil, ErrLine(media.CodeManifestDrift, 0, "segment count too large")
	}
	out := make([]dashDuration, 0, want)
	for i := int64(0); i < want; i++ {
		d := tmpl.Duration
		if remaining := total - i*tmpl.Duration; remaining < d {
			d = remaining
		}
		out = append(out, dashDuration{start: i * tmpl.Duration, duration: d})
	}
	return out, nil
}

func repID(r dashRepresentation) string {
	if r.ID != "" {
		return r.ID
	}
	return "rep"
}

// selectDASHRepresentation picks the first representation bearing a segment
// template, inheriting the adaptation set's template when absent.
func selectDASHRepresentation(mpd *dashMPD) (dashRepresentation, *dashTemplate, dashPeriod, error) {
	for _, p := range mpd.Periods {
		for _, a := range p.AdaptationSets {
			for _, r := range a.Representations {
				tmpl := r.SegmentTemplate
				if tmpl == nil {
					tmpl = a.SegmentTemplate
				}
				if tmpl == nil {
					continue
				}
				if r.Timescale == 0 {
					switch {
					case tmpl.Timescale > 0:
						r.Timescale = tmpl.Timescale
					case a.SegmentTemplate != nil && a.SegmentTemplate.Timescale > 0:
						r.Timescale = a.SegmentTemplate.Timescale
					}
				}
				return r, tmpl, p, nil
			}
		}
	}
	return dashRepresentation{}, nil, dashPeriod{}, ErrLine(media.CodeInput, 0, "MPD has no usable representation")
}

// lineOffset converts an XML syntax error's line number into a byte offset so
// the structured error points at the offending line.
func lineOffset(src string, err error) int64 {
	se, ok := err.(*xml.SyntaxError)
	if !ok || se.Line <= 1 {
		return 0
	}
	line := 1
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			line++
			if line == se.Line {
				return int64(i + 1)
			}
		}
	}
	return 0
}
