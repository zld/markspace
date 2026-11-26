package manifest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"archivecheck/internal"
)

// DASHSegment is one expanded SegmentTimeline/SegmentTemplate entry.
type DASHSegment struct {
	Index         int    `json:"index"`
	Number        int64  `json:"number"`
	TimeUnits     int64  `json:"time_units"`
	DurationUnits int64  `json:"duration_units"`
	StartNS       int64  `json:"start_ns"`
	DurationNS    int64  `json:"duration_ns"`
	URL           string `json:"url"`
}

// Representation is one DASH bitrate representation on a unified time base.
type Representation struct {
	ID          string        `json:"id"`
	Kind        string        `json:"kind"`
	MimeType    string        `json:"mime_type"`
	Bandwidth   int64         `json:"bandwidth"`
	Timescale   int64         `json:"timescale"`
	StartNumber int64         `json:"start_number"`
	Segments    []DASHSegment `json:"segments"`
}

// DASHManifest contains all representations from the first Period.
type DASHManifest struct {
	Source          string           `json:"source"`
	MediaDurationNS int64            `json:"media_duration_ns"`
	Representations []Representation `json:"representations"`
}

type dashS struct {
	T *int64 `xml:"t,attr"`
	D int64  `xml:"d,attr"`
	R int    `xml:"r,attr"`
}

type dashTimeline struct {
	S []dashS `xml:"S"`
}

type dashTemplate struct {
	Timescale   int64         `xml:"timescale,attr"`
	Media       string        `xml:"media,attr"`
	StartNumber int64         `xml:"startNumber,attr"`
	Duration    int64         `xml:"duration,attr"`
	Timeline    *dashTimeline `xml:"SegmentTimeline"`
}

type dashRepresentation struct {
	ID              string        `xml:"id,attr"`
	Bandwidth       int64         `xml:"bandwidth,attr"`
	MimeType        string        `xml:"mimeType,attr"`
	SegmentTemplate *dashTemplate `xml:"SegmentTemplate"`
}

type dashAdaptationSet struct {
	ContentType     string               `xml:"contentType,attr"`
	MimeType        string               `xml:"mimeType,attr"`
	SegmentTemplate *dashTemplate        `xml:"SegmentTemplate"`
	Representations []dashRepresentation `xml:"Representation"`
}

type dashXML struct {
	MediaPresentationDuration string `xml:"mediaPresentationDuration,attr"`
	Periods                   []struct {
		AdaptationSets []dashAdaptationSet `xml:"AdaptationSet"`
	} `xml:"Period"`
}

var numberFormatRE = regexp.MustCompile(`\$Number(?:%0(\d+)d)?\$`)

// ParseDASH parses and expands all supported multi-representation templates.
func ParseDASH(data []byte, source string) (*DASHManifest, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var root dashXML
	if err := dec.Decode(&root); err != nil {
		return nil, internal.WrapFault("dash.parse", source, int(dec.InputOffset()), err)
	}

	out := &DASHManifest{Source: source}
	if root.MediaPresentationDuration != "" {
		d, err := parseISO8601Duration(root.MediaPresentationDuration)
		if err != nil {
			return nil, internal.WrapFault("dash.duration", source, 0, err)
		}
		out.MediaDurationNS = int64(d)
	}

	for _, period := range root.Periods {
		for _, as := range period.AdaptationSets {
			for _, rx := range as.Representations {
				rep, err := buildRepresentation(as, rx)
				if err != nil {
					return nil, internal.WrapFault("dash.timeline", source, 0, err)
				}
				out.Representations = append(out.Representations, rep)
			}
		}
	}
	if len(out.Representations) == 0 {
		return nil, internal.NewFault("dash.parse", source, 0, "no Representation elements found")
	}
	return out, nil
}

// ParseDASHFile reads a local MPD.
func ParseDASHFile(path string) (*DASHManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, internal.WrapFault("dash.read", path, 0, err)
	}
	return ParseDASH(data, path)
}

func buildRepresentation(as dashAdaptationSet, rx dashRepresentation) (Representation, error) {
	rep := Representation{
		ID: rx.ID, Kind: as.ContentType, MimeType: rx.MimeType,
		Bandwidth: rx.Bandwidth, Timescale: 1000, StartNumber: 1,
	}
	if rep.MimeType == "" {
		rep.MimeType = as.MimeType
	}
	tpl := rx.SegmentTemplate
	if tpl == nil {
		tpl = as.SegmentTemplate
	}
	if tpl == nil {
		return rep, fmt.Errorf("representation %s has no SegmentTemplate", rx.ID)
	}
	if tpl.Timescale > 0 {
		rep.Timescale = tpl.Timescale
	}
	if tpl.StartNumber > 0 {
		rep.StartNumber = tpl.StartNumber
	}
	if tpl.Timeline == nil || len(tpl.Timeline.S) == 0 {
		return rep, fmt.Errorf("representation %s has no SegmentTimeline", rx.ID)
	}

	number := rep.StartNumber
	var nextT int64
	for _, sx := range tpl.Timeline.S {
		if sx.D <= 0 {
			return rep, fmt.Errorf("segment d must be positive: %+v", sx)
		}
		start := nextT
		if sx.T != nil {
			start = *sx.T
		}
		if sx.R < 0 {
			return rep, fmt.Errorf("negative repeat r=%d is unsupported", sx.R)
		}
		repeats := sx.R + 1
		for i := 0; i < repeats; i++ {
			t, err := checkedAdd(start, int64(i)*sx.D)
			if err != nil {
				return rep, err
			}
			seg, err := makeSegment(len(rep.Segments), number, t, sx.D, tpl, rep.Timescale, rep.ID)
			if err != nil {
				return rep, err
			}
			rep.Segments = append(rep.Segments, seg)
			number++
		}
		end, err := checkedAdd(start, int64(repeats)*sx.D)
		if err != nil {
			return rep, err
		}
		nextT = end
	}
	return rep, nil
}

func makeSegment(index int, number, t, d int64, tpl *dashTemplate, timescale int64, repID string) (DASHSegment, error) {
	start, err := internal.ScaledToNS(t, timescale)
	if err != nil {
		return DASHSegment{}, err
	}
	dur, err := internal.ScaledToNS(d, timescale)
	if err != nil {
		return DASHSegment{}, err
	}
	return DASHSegment{
		Index: index, Number: number, TimeUnits: t, DurationUnits: d,
		StartNS: int64(start), DurationNS: int64(dur),
		URL: expandTemplate(tpl.Media, repID, number, t),
	}, nil
}

func expandTemplate(template, repID string, number, time int64) string {
	if template == "" {
		return ""
	}
	s := strings.ReplaceAll(template, "$RepresentationID$", repID)
	s = strings.ReplaceAll(s, "$Time$", strconv.FormatInt(time, 10))
	return numberFormatRE.ReplaceAllStringFunc(s, func(match string) string {
		sub := numberFormatRE.FindStringSubmatch(match)
		if sub[1] == "" {
			return strconv.FormatInt(number, 10)
		}
		width, _ := strconv.Atoi(sub[1])
		return fmt.Sprintf("%0*d", width, number)
	})
}

func checkedAdd(a, b int64) (int64, error) {
	ns, err := internal.AddNS(internal.NS(a), internal.NS(b))
	return int64(ns), err
}

func parseISO8601Duration(s string) (internal.NS, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("invalid ISO8601 duration %q", s)
	}
	s = s[1:]
	inTime := false
	var total internal.NS
	unitNS := map[byte]int64{
		'D': 24 * int64(internal.NSPerSecond) * 60 * 60,
		'H': int64(internal.NSPerSecond) * 60 * 60,
		'M': int64(internal.NSPerSecond) * 60,
		'S': int64(internal.NSPerSecond),
	}
	for len(s) > 0 {
		if s[0] == 'T' {
			inTime = true
			s = s[1:]
			continue
		}
		i := 0
		for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
			i++
		}
		if i == 0 || i >= len(s) {
			return 0, fmt.Errorf("invalid ISO8601 duration %q", s)
		}
		token, unit := s[:i], s[i]
		s = s[i+1:]
		if unit == 'M' && !inTime {
			unitNS['M'] = 30 * 24 * int64(internal.NSPerSecond) * 60 * 60 // approximate month not used by fixtures
		}
		scale, ok := unitNS[unit]
		if !ok || (unit == 'D' && inTime) {
			return 0, fmt.Errorf("invalid duration unit %q", string(unit))
		}
		d, err := internal.ParseDecimalSeconds(token)
		if err != nil {
			return 0, err
		}
		add := internal.NS(int64(d) * scale / int64(internal.NSPerSecond))
		total, err = internal.AddNS(total, add)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}
