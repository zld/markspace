package manifest

import (
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"mediascan/internal/fault"
)

// dashTemplate captures the attributes that drive $Number$ expansion.
type dashTemplate struct {
	media       string
	timescale   uint32
	duration    uint64
	startNumber uint64
}

// ParseDASH parses a minimal MPD using SegmentTemplate with the $Number$
// identifier. SegmentTimeline is also accepted. Offsets record the line
// of the originating element so faults point back into the MPD text.
func ParseDASH(data []byte) (*Manifest, *fault.Log) {
	p := newParser()
	m := &Manifest{Format: FormatDASH, TimeScale: 1}

	dec := xml.NewDecoder(bytes.NewReader(data))
	var tpl dashTemplate
	tpl.startNumber = 1
	var timelineEntries []timelineEntry
	var repID string
	var currentLine int64

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			p.log.Add(fault.Record{
				Stage: fault.StageManifest, Severity: fault.SeverityError,
				Kind:         fault.KindRoundtripMismatch,
				SourceOffset: currentLine,
				Message:      "malformed MPD: " + err.Error(),
			})
			break
		}
		if se, ok := tok.(xml.StartElement); ok {
			currentLine = dec.InputOffset()
			switch se.Name.Local {
			case "SegmentTemplate":
				readDashTemplate(&tpl, se)
				if tpl.timescale == 0 {
					tpl.timescale = 1
				}
			case "Representation":
				repID = attr(se, "id")
			case "S":
				timelineEntries = append(timelineEntries, readTimelineEntry(se))
			}
		}
	}

	m.TimeScale = tpl.timescale
	if tpl.duration > 0 {
		m.TargetDuration = float64(tpl.duration) / float64(tpl.timescale)
	}

	if len(timelineEntries) > 0 {
		expandTimeline(m, p, tpl, timelineEntries, repID)
	} else if tpl.duration > 0 {
		// Without a SegmentTimeline the MPD must declare a bounded
		// mediaPresentationDuration on the MPD element.
		dur := readPresentationDuration(data)
		count := uint64(0)
		if dur > 0 {
			scale := tpl.timescale
			if scale == 0 {
				scale = 1
			}
			count = (uint64(dur*float64(scale)) + tpl.duration - 1) / tpl.duration
		}
		for i := uint64(0); i < count; i++ {
			appendDASHSegment(m, p, tpl, tpl.startNumber+i, i, repID)
		}
	}
	m.StartSeq = tpl.startNumber
	p.checkSequences(m.Segments)
	return m, p.log
}

type timelineEntry struct {
	d    uint64
	r    int
	n    uint64
	hasN bool
}

func readTimelineEntry(se xml.StartElement) timelineEntry {
	e := timelineEntry{} // r defaults to 0 => the element appears once
	for _, a := range se.Attr {
		switch a.Name.Local {
		case "d":
			e.d, _ = strconv.ParseUint(a.Value, 10, 64)
		case "r":
			e.r, _ = strconv.Atoi(a.Value)
		case "n":
			e.n, _ = strconv.ParseUint(a.Value, 10, 64)
			e.hasN = true
		}
	}
	return e
}

func expandTimeline(m *Manifest, p parser, tpl dashTemplate, entries []timelineEntry, repID string) {
	var seqIndex uint64
	// Without explicit n attributes the timeline starts at startNumber.
	implicitN := tpl.startNumber
	for _, e := range entries {
		// Per DASH, r is the repeat count: the element appears r+1 times.
		repeats := e.r + 1
		// An explicit n anchors this element's first sequence number;
		// absent n values continue the implicit timeline counter.
		n := implicitN
		if e.hasN {
			n = e.n
		}
		for j := 0; j < repeats; j++ {
			seq := n + uint64(j)
			appendDASHSegment(m, p, tpl, seq, seqIndex, repID)
			seqIndex++
		}
		implicitN = n + uint64(repeats)
	}
}

func appendDASHSegment(m *Manifest, p parser, tpl dashTemplate, seq, ordinal uint64, repID string) {
	uri := expandNumberTemplate(tpl.media, repID, seq)
	dur := float64(tpl.duration) / float64(tpl.timescale)
	start := float64(ordinal) * dur
	m.Segments = append(m.Segments, Segment{
		Index: int(ordinal), MediaSeq: seq, URI: uri,
		Duration: dur, Start: start, End: start + dur,
		SourceOffset: int64(ordinal),
	})
}

func readDashTemplate(tpl *dashTemplate, se xml.StartElement) {
	for _, a := range se.Attr {
		switch a.Name.Local {
		case "media":
			tpl.media = a.Value
		case "timescale":
			v, _ := strconv.ParseUint(a.Value, 10, 32)
			tpl.timescale = uint32(v)
		case "duration":
			tpl.duration, _ = strconv.ParseUint(a.Value, 10, 64)
		case "startNumber":
			tpl.startNumber, _ = strconv.ParseUint(a.Value, 10, 64)
		}
	}
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// expandNumberTemplate substitutes $Number$ and $RepresentationID$.
func expandNumberTemplate(pattern, repID string, number uint64) string {
	out := pattern
	for {
		i := strings.Index(out, "$Number")
		if i < 0 {
			break
		}
		rest := out[i+len("$Number"):]
		end := strings.Index(rest, "$")
		if end < 0 {
			break
		}
		out = out[:i] + strconv.FormatUint(number, 10) + rest[end+1:]
	}
	out = strings.ReplaceAll(out, "$RepresentationID$", repID)
	return out
}

// readPresentationDuration extracts mediaPresentationDuration="PTxS"
// without pulling in a full ISO-8601 dependency.
func readPresentationDuration(data []byte) float64 {
	s := string(data)
	key := "mediaPresentationDuration="
	i := strings.Index(s, key)
	if i < 0 {
		return 0
	}
	rest := s[i+len(key):]
	quote := strings.IndexAny(rest, "\"'")
	if quote < 0 {
		return 0
	}
	value := rest[quote+1:]
	if end := strings.IndexAny(value, "\"'"); end >= 0 {
		value = value[:end]
	}
	value = strings.TrimSuffix(strings.TrimPrefix(value, "PT"), "S")
	f, _ := strconv.ParseFloat(value, 64)
	return f
}
