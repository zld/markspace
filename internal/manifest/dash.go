package manifest

import (
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"archivecheck/internal/model"
)

// mpd is the subset of the DASH MPD schema the archive kernel consumes.
type mpd struct {
	XMLName                   xml.Name    `xml:"MPD"`
	Type                      string      `xml:"type,attr"`
	MediaPresentationDuration string      `xml:"mediaPresentationDuration,attr"`
	MinBufferTime             string      `xml:"minBufferTime,attr"`
	BaseURL                   string      `xml:"BaseURL"`
	Periods                   []mpdPeriod `xml:"Period"`
}

type mpdPeriod struct {
	ID             string          `xml:"id,attr"`
	Start          string          `xml:"start,attr"`
	Duration       string          `xml:"duration,attr"`
	BaseURL        string          `xml:"BaseURL"`
	AdaptationSets []mpdAdaptation `xml:"AdaptationSet"`
}

type mpdAdaptation struct {
	ID              string          `xml:"id,attr"`
	ContentType     string          `xml:"contentType,attr"`
	MimeType        string          `xml:"mimeType,attr"`
	SegmentTemplate *mpdSegmentTmpl `xml:"SegmentTemplate"`
	Representations []mpdRepr       `xml:"Representation"`
}

type mpdRepr struct {
	ID              string          `xml:"id,attr"`
	Bandwidth       int64           `xml:"bandwidth,attr"`
	BaseURL         string          `xml:"BaseURL"`
	SegmentTemplate *mpdSegmentTmpl `xml:"SegmentTemplate"`
}

type mpdSegmentTmpl struct {
	Timescale              int64               `xml:"timescale,attr"`
	Duration               int64               `xml:"duration,attr"`
	StartNumber            int64               `xml:"startNumber,attr"`
	PresentationTimeOffset int64               `xml:"presentationTimeOffset,attr"`
	Media                  string              `xml:"media,attr"`
	Initialization         string              `xml:"initialization,attr"`
	Timeline               *mpdSegmentTimeline `xml:"SegmentTimeline"`
}

type mpdSegmentTimeline struct {
	// S entries are in document order.
	S []mpdS `xml:"S"`
}

type mpdS struct {
	T string `xml:"t,attr"`
	D int64  `xml:"d,attr"`
	R int64  `xml:"r,attr"`
	// RSet distinguishes an absent r (which means 1) from r="0".
	RSet bool `xml:"-"`
}

// UnmarshalXML records whether the r attribute was present.
func (s *mpdS) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type rawS struct {
		T string `xml:"t,attr"`
		D int64  `xml:"d,attr"`
		R int64  `xml:"r,attr"`
	}
	var raw rawS
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	s.T, s.D = raw.T, raw.D
	for _, a := range start.Attr {
		if a.Name.Local == "r" {
			s.R, s.RSet = raw.R, true
		}
	}
	if !s.RSet {
		s.R = 1
	}
	return nil
}

// ParseDASH parses an MPD and returns a unified plan.
func ParseDASH(name string, r io.Reader, opts Options) (*Plan, error) {
	raw := &mpd{}
	dec := xml.NewDecoder(r)
	dec.Strict = true
	if err := dec.Decode(raw); err != nil {
		return nil, model.NewFault(model.CodeManifestSyntax, model.SeverityError,
			model.Source{Layer: "manifest", File: name, Offset: -1}, "invalid MPD XML: %v", err)
	}
	v := NewValidator(name)
	plan := buildDASHPlan(name, raw, opts, v)
	if v.faults.Len() > 0 {
		return nil, v.Err()
	}
	return plan, nil
}

// parseMPDDuration parses an xs:duration ("PT6.006S", "PT0H1M0.5S") into an
// exact rational number of seconds.
func parseMPDDuration(text string) (model.Rational, bool, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return model.Rational{}, false, nil
	}
	if !strings.HasPrefix(s, "P") {
		return model.Rational{}, false, errBadDuration(s)
	}
	rest := s[1:]
	if i := strings.IndexAny(rest, "T"); i >= 0 {
		rest = rest[i+1:]
	} else if strings.ContainsAny(rest, "SYMWD") {
		return model.Rational{}, false, errBadDuration(s)
	}
	var seconds model.Rational
	seen := false
	for rest != "" {
		numEnd := strings.IndexAny(rest, "HMS")
		if numEnd < 0 {
			return model.Rational{}, false, errBadDuration(s)
		}
		value, unit := rest[:numEnd], rest[numEnd]
		rest = rest[numEnd+1:]
		r, err := ParseDecimalSeconds(value)
		if err != nil {
			return model.Rational{}, false, errBadDuration(s)
		}
		switch unit {
		case 'H':
			r, err = mulRationalR(r, 3600)
		case 'M':
			r, err = mulRationalR(r, 60)
		case 'S':
		}
		if err != nil {
			return model.Rational{}, false, errBadDuration(s)
		}
		seconds, err = seconds.Add(r)
		if err != nil {
			return model.Rational{}, false, errBadDuration(s)
		}
		seen = true
	}
	if !seen {
		return model.Rational{}, false, errBadDuration(s)
	}
	return seconds, true, nil
}

type badDuration string

func (b badDuration) Error() string { return "invalid xs:duration " + strconv.Quote(string(b)) }

func errBadDuration(s string) error { return badDuration(s) }

func mulRationalR(r model.Rational, k int64) (model.Rational, error) {
	if r.Num != 0 && (r.Num > model.MaxTicks/k || r.Num < -model.MaxTicks/k) {
		return model.Rational{}, model.ErrTimeOverflow
	}
	return model.NewRational(r.Num*k, r.Den)
}
