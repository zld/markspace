package manifest

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ParseDASH reads and parses an MPD file.
func ParseDASH(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mpd %s: %w", path, err)
	}
	return ParseDASHBytes(path, data)
}

// ParseDASHBytes parses an in-memory MPD. Offsets reported in issues are byte
// offsets into data; lines are 1-based.
func ParseDASHBytes(name string, data []byte) (*Manifest, error) {
	raw, err := parseMPD(data)
	m := &Manifest{Kind: "dash", File: name}
	if m.File == "" {
		m.File = "mpd"
	}
	if err != nil {
		m.Issues = append(m.Issues, issueAt(m.File, 0, 0, "E_PARSE", fmt.Sprintf("mpd XML: %v", err)))
		return m, fmt.Errorf("parse mpd %s: %w", name, err)
	}
	expandDASH(m, raw, data)
	m.Segments = sortSegments(m.Segments)
	m.SortIssues()
	return m, nil
}

// raw MPD shapes filled by the streaming tokenizer.
type dashDoc struct {
	duration string
	periods  []*dashPeriod
}

type dashPeriod struct {
	id, start, duration string
	sets                []*dashAdaptation
	line, offset        int
}

type dashAdaptation struct {
	duration string
	reps     []*dashRep
}

type dashRep struct {
	id, bandwidth string
	line, offset  int
	baseURL       string
	tmpl          *dashTemplate
	list          *dashList
	base          *dashBase
}

type dashTemplate struct {
	timescale, duration, startNumber, media, init string
	line, offset                                  int
	timeline                                      []dashS
}

type dashS struct {
	t, d, r string
	hasT    bool
	line    int
	offset  int64
}

type dashList struct {
	timescale string
	duration  string
	urls      []dashURLRef
}

func (l *dashList) scale() uint32 {
	if l == nil || l.timescale == "" {
		return 1
	}
	n, err := strconv.ParseUint(l.timescale, 10, 32)
	if err != nil || n == 0 {
		return 1
	}
	return uint32(n)
}

type dashURLRef struct {
	media, duration string
	line, offset    int
}

type dashBase struct {
	timescale  string
	indexRange string
}

// parseMPD tokenizes the document, recording element byte offsets.
func parseMPD(data []byte) (*dashDoc, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	doc := &dashDoc{duration: ""}
	var period *dashPeriod
	var set *dashAdaptation
	var rep *dashRep
	var tmpl *dashTemplate
	var list *dashList
	var base *dashBase
	captureBase := false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			off := int(dec.InputOffset())
			line := lineAt(data, off)
			switch t.Name.Local {
			case "MPD":
				doc.duration = attr(t, "mediaPresentationDuration")
			case "Period":
				period = &dashPeriod{id: attr(t, "id"), start: attr(t, "start"),
					duration: attr(t, "duration"), line: line, offset: off}
				doc.periods = append(doc.periods, period)
			case "AdaptationSet":
				set = &dashAdaptation{duration: attr(t, "duration")}
				if period != nil {
					period.sets = append(period.sets, set)
				}
			case "Representation":
				rep = &dashRep{id: attr(t, "id"), bandwidth: attr(t, "bandwidth"), line: line, offset: off}
				if set != nil {
					set.reps = append(set.reps, rep)
				}
			case "BaseURL":
				captureBase = true
			case "SegmentTemplate":
				tmpl = &dashTemplate{
					timescale: attr(t, "timescale"), duration: attr(t, "duration"),
					startNumber: attr(t, "startNumber"), media: attr(t, "media"),
					init: attr(t, "initialization"), line: line, offset: off,
				}
				if rep != nil {
					rep.tmpl = tmpl
				}
			case "SegmentTimeline":
				// timeline entries follow as direct children
			case "S":
				if tmpl != nil {
					tmpl.timeline = append(tmpl.timeline, dashS{
						t: attr(t, "t"), hasT: attrPtr(t, "t") != nil,
						d: attr(t, "d"), r: attr(t, "r"),
						line: line, offset: int64(off),
					})
				}
			case "SegmentList":
				list = &dashList{timescale: attr(t, "timescale"), duration: attr(t, "duration")}
				if rep != nil {
					rep.list = list
				}
			case "SegmentURL":
				if list != nil {
					list.urls = append(list.urls, dashURLRef{
						media: attr(t, "media"), duration: attr(t, "mediaDuration"),
						line: line, offset: off,
					})
				}
			case "SegmentBase":
				base = &dashBase{timescale: attr(t, "timescale"), indexRange: attr(t, "indexRange")}
				if rep != nil {
					rep.base = base
				}
			}
		case xml.CharData:
			if captureBase && rep != nil {
				rep.baseURL += string(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "BaseURL":
				captureBase = false
				if rep != nil {
					rep.baseURL = strings.TrimSpace(rep.baseURL)
				}
			case "Representation":
				rep = nil
				tmpl, list, base = nil, nil, nil
			case "AdaptationSet":
				set = nil
			case "Period":
				period = nil
			}
		}
	}
	if len(doc.periods) == 0 {
		return nil, fmt.Errorf("MPD declares no Period")
	}
	return doc, nil
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func attrPtr(e xml.StartElement, name string) *string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			v := a.Value
			return &v
		}
	}
	return nil
}
