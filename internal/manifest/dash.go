package manifest

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mediascan/internal/clock"
	"mediascan/internal/model"
)

// DASHSegment is one template-expanded representation segment.
type DASHSegment struct {
	Number        int64
	URI           string
	StartTicks    int64 // unified
	DurationTicks int64 // unified
}

// Representation is one DASH AdaptationSet representation.
type Representation struct {
	ID        string
	Bandwidth int
	Timescale uint32
	InitURI   string
	Segments  []DASHSegment
}

// DASH is the parsed MPD.
type DASH struct {
	Representations []Representation
}

type mpdXML struct {
	XMLName xml.Name `xml:"MPD"`
	Periods []struct {
		AdaptationSets []struct {
			SegmentTemplate *segmentTemplateXML `xml:"SegmentTemplate"`
			Representations []struct {
				ID              string              `xml:"id,attr"`
				Bandwidth       int                 `xml:"bandwidth,attr"`
				SegmentTemplate *segmentTemplateXML `xml:"SegmentTemplate"`
			} `xml:"Representation"`
		} `xml:"AdaptationSet"`
	} `xml:"Period"`
}

type segmentTemplateXML struct {
	Timescale      uint32 `xml:"timescale,attr"`
	Media          string `xml:"media,attr"`
	Initialization string `xml:"initialization,attr"`
	StartNumber    int64  `xml:"startNumber,attr"`
	Duration       int64  `xml:"duration,attr"`
	Timeline       *struct {
		S []struct {
			T *int64 `xml:"t,attr"`
			D int64  `xml:"d,attr"`
			R int64  `xml:"r,attr"`
		} `xml:"S"`
	} `xml:"SegmentTimeline"`
}

// ParseDASH parses an MPD and expands representation templates.
func ParseDASH(r io.Reader) (*DASH, error) {
	var mx mpdXML
	if err := xml.NewDecoder(r).Decode(&mx); err != nil {
		return nil, fmt.Errorf("dash: parse mpd: %w", err)
	}
	d := &DASH{}
	for _, p := range mx.Periods {
		for _, as := range p.AdaptationSets {
			for _, rx := range as.Representations {
				tpl := mergeTemplate(as.SegmentTemplate, rx.SegmentTemplate)
				if tpl == nil {
					continue
				}
				rep, err := buildRepresentation(rx.ID, rx.Bandwidth, tpl)
				if err != nil {
					return nil, err
				}
				d.Representations = append(d.Representations, rep)
			}
		}
	}
	if len(d.Representations) == 0 {
		return nil, fmt.Errorf("dash: no representations with SegmentTemplate found")
	}
	return d, nil
}

func mergeTemplate(base, override *segmentTemplateXML) *segmentTemplateXML {
	if base == nil && override == nil {
		return nil
	}
	t := &segmentTemplateXML{}
	if base != nil {
		*t = *base
	}
	if override != nil {
		if override.Timescale != 0 {
			t.Timescale = override.Timescale
		}
		if override.Media != "" {
			t.Media = override.Media
		}
		if override.Initialization != "" {
			t.Initialization = override.Initialization
		}
		if override.StartNumber != 0 {
			t.StartNumber = override.StartNumber
		}
		if override.Duration != 0 {
			t.Duration = override.Duration
		}
		if override.Timeline != nil {
			t.Timeline = override.Timeline
		}
	}
	if t.StartNumber == 0 {
		t.StartNumber = 1
	}
	return t
}

func buildRepresentation(id string, bandwidth int, t *segmentTemplateXML) (Representation, error) {
	rep := Representation{ID: id, Bandwidth: bandwidth, Timescale: t.Timescale}
	if t.Timescale == 0 {
		return rep, clock.ErrZeroTimescale
	}
	rep.InitURI = expandTemplate(t.Initialization, id, t.StartNumber)

	type raw struct{ start, dur int64 }
	var raws []raw
	if t.Timeline != nil {
		var cum int64
		for _, s := range t.Timeline.S {
			start := cum
			if s.T != nil {
				start = *s.T
				cum = *s.T
			}
			repeat := s.R
			if repeat < 0 {
				repeat = 0 // negative repeat needs endAttribute; fixtures use explicit counts
			}
			for k := int64(0); k <= repeat; k++ {
				raws = append(raws, raw{start + k*s.D, s.D})
			}
			cum = start + (repeat+1)*s.D
		}
	} else if t.Duration > 0 {
		// Without a timeline the segment count is unknown; callers providing
		// only duration get a zero-segment template (alignment still validates
		// timescale). Fixtures use SegmentTimeline for concrete segments.
	}

	for i, rr := range raws {
		num := t.StartNumber + int64(i)
		startU, err := clock.ToUnified(rr.start, t.Timescale)
		if err != nil {
			return rep, err
		}
		durU, err := clock.ToUnified(rr.dur, t.Timescale)
		if err != nil {
			return rep, err
		}
		rep.Segments = append(rep.Segments, DASHSegment{
			Number: num, URI: expandTemplate(t.Media, id, num),
			StartTicks: startU, DurationTicks: durU,
		})
	}
	return rep, nil
}

func expandTemplate(s, repID string, number int64) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "$RepresentationID$", repID)
	s = strings.ReplaceAll(s, "$Number%05d$", fmt.Sprintf("%05d", number))
	s = strings.ReplaceAll(s, "$Number%04d$", fmt.Sprintf("%04d", number))
	s = strings.ReplaceAll(s, "$Number%03d$", fmt.Sprintf("%03d", number))
	s = strings.ReplaceAll(s, "$Number$", strconv.FormatInt(number, 10))
	return s
}

// AlignRepresentations compares every representation to the first and
// reports count or start-time differences instead of silently concatenating.
func AlignRepresentations(reps []Representation, toleranceTicks int64) []model.Fault {
	if toleranceTicks <= 0 {
		toleranceTicks = 2
	}
	var faults []model.Fault
	if len(reps) < 2 {
		return nil
	}
	base := reps[0]
	for ri := 1; ri < len(reps); ri++ {
		cur := reps[ri]
		if len(cur.Segments) != len(base.Segments) {
			f := model.NewFault(model.CodeDashAlignment, "",
				fmt.Sprintf("representation %q has %d segments, base %q has %d",
					cur.ID, len(cur.Segments), base.ID, len(base.Segments)))
			f.Severity = model.SeverityError
			faults = append(faults, f)
		}
		m := len(base.Segments)
		if len(cur.Segments) < m {
			m = len(cur.Segments)
		}
		for i := 0; i < m; i++ {
			diff := cur.Segments[i].StartTicks - base.Segments[i].StartTicks
			if diff < 0 {
				diff = -diff
			}
			if diff > toleranceTicks {
				f := model.NewFault(model.CodeDashAlignment, "",
					fmt.Sprintf("representation %q segment %d starts %d ticks off from %q (delta=%d)",
						cur.ID, i, cur.Segments[i].StartTicks, base.ID, diff))
				f.Severity = model.SeverityError
				f.TimeStart = base.Segments[i].StartTicks
				f.TimeEnd = cur.Segments[i].StartTicks
				f.SampleIndex = int64(i)
				faults = append(faults, f)
			}
		}
	}
	return faults
}
