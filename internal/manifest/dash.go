package manifest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"archive-lab/internal/media"
)

type mpdXML struct {
	XMLName  xml.Name    `xml:"MPD"`
	Type     string      `xml:"type,attr"`
	Media    string      `xml:"mediaPresentationDuration,attr"`
	Profiles string      `xml:"profiles,attr"`
	BaseURL  string      `xml:"BaseURL"`
	Periods  []periodXML `xml:"Period"`
}

type periodXML struct {
	ID             string        `xml:"id,attr"`
	Start          string        `xml:"start,attr"`
	Duration       string        `xml:"duration,attr"`
	BaseURL        string        `xml:"BaseURL"`
	AdaptationSets []adaptSetXML `xml:"AdaptationSet"`
}

type adaptSetXML struct {
	ID              string   `xml:"id,attr"`
	MimeType        string   `xml:"mimeType,attr"`
	Codecs          string   `xml:"codecs,attr"`
	Language        string   `xml:"lang,attr"`
	ContentType     string   `xml:"contentType,attr"`
	SegmentTemplate *tmplXML `xml:"SegmentTemplate"`
	Representations []repXML `xml:"Representation"`
}

type repXML struct {
	ID                string   `xml:"id,attr"`
	Bandwidth         string   `xml:"bandwidth,attr"`
	AverageBandwidth  string   `xml:"averageBandwidth,attr"`
	Codecs            string   `xml:"codecs,attr"`
	MimeType          string   `xml:"mimeType,attr"`
	Width             string   `xml:"width,attr"`
	Height            string   `xml:"height,attr"`
	FrameRate         string   `xml:"frameRate,attr"`
	AudioSamplingRate string   `xml:"audioSamplingRate,attr"`
	SegmentTemplate   *tmplXML `xml:"SegmentTemplate"`
	SegmentList       *listXML `xml:"SegmentList"`
}

type tmplXML struct {
	Timescale              string       `xml:"timescale,attr"`
	Duration               string       `xml:"duration,attr"`
	StartNumber            string       `xml:"startNumber,attr"`
	PresentationTimeOffset string       `xml:"presentationTimeOffset,attr"`
	Media                  string       `xml:"media,attr"`
	Initialization         string       `xml:"initialization,attr"`
	Timeline               *timelineXML `xml:"SegmentTimeline"`
}

type listXML struct {
	Timescale      string      `xml:"timescale,attr"`
	Duration       string      `xml:"duration,attr"`
	StartNumber    string      `xml:"startNumber,attr"`
	Initialization *initXML    `xml:"Initialization"`
	SegmentURLs    []segURLXML `xml:"SegmentURL"`
}

type initXML struct {
	SourceURL string `xml:"sourceURL,attr"`
}

type segURLXML struct {
	Media string `xml:"media,attr"`
}

type timelineXML struct {
	Items []timelineItemXML `xml:"S"`
}

type timelineItemXML struct {
	T string `xml:"t,attr"`
	D string `xml:"d,attr"`
	R string `xml:"r,attr"`
}

// ParseDASH parses an ISO/IEC 23009-1 MPD. Only SegmentTemplate (with or
// without SegmentTimeline) and SegmentList are supported; unsupported forms
// produce a located error instead of a partial representation.
func ParseDASH(path string, data []byte, limits Limits) (*Playlist, error) {
	limits = limits.resolved()
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	var doc mpdXML
	if err := dec.Decode(&doc); err != nil {
		return nil, media.New(media.StageParse, "dash", "malformed XML: %v", err).WithPath(path).
			WithOffset(offsetOfXMLError(data, err))
	}
	pl := &Playlist{Path: path, Kind: KindDASH}
	if doc.Type != "" && doc.Type != "static" {
		return nil, media.New(media.StageParse, "dash", "dynamic MPD (type=%s) is not supported", doc.Type).WithPath(path)
	}
	pl.BaseURL = strings.TrimSpace(doc.BaseURL)
	if pl.BaseURL != "" && !isLocalRef(pl.BaseURL) {
		return nil, media.New(media.StageParse, "dash", "remote BaseURL %q is not supported", pl.BaseURL).WithPath(path)
	}
	if len(doc.Periods) == 0 {
		return nil, media.New(media.StageParse, "dash", "MPD has no Period").WithPath(path)
	}
	var totalNs int64 = -1
	if doc.Media != "" {
		ns, err := parseISODurationNanos(doc.Media)
		if err != nil {
			return nil, media.New(media.StageParse, "dash", "invalid mediaPresentationDuration %q", doc.Media).WithPath(path)
		}
		totalNs = ns
	}

	for pi := range doc.Periods {
		period := &doc.Periods[pi]
		periodBase := joinLocal(pl.BaseURL, strings.TrimSpace(period.BaseURL))
		for ai := range period.AdaptationSets {
			set := &period.AdaptationSets[ai]
			for ri := range set.Representations {
				rep := &set.Representations[ri]
				r, err := buildRepresentation(path, set, rep, periodBase, totalNs, limits)
				if err != nil {
					return nil, err
				}
				pl.Representations = append(pl.Representations, *r)
			}
		}
	}
	if len(pl.Representations) == 0 {
		return nil, media.New(media.StageParse, "dash", "MPD has no Representations").WithPath(path)
	}
	return pl, nil
}

func buildRepresentation(path string, set *adaptSetXML, rep *repXML, base string, totalNs int64, limits Limits) (*Representation, *media.Error) {
	r := &Representation{
		ID:        rep.ID,
		Codecs:    firstNonEmpty(rep.Codecs, set.Codecs),
		MimeType:  firstNonEmpty(rep.MimeType, set.MimeType),
		Language:  set.Language,
		FrameRate: rep.FrameRate,
	}
	if rep.ID == "" {
		return nil, media.New(media.StageParse, "dash", "Representation without id in adaptation set %q", set.ID).WithPath(path)
	}
	if rep.Bandwidth == "" {
		return nil, media.New(media.StageParse, "dash", "representation %q is missing bandwidth", rep.ID).WithPath(path)
	}
	var err error
	if r.Bandwidth, err = parsePositiveInt64(rep.Bandwidth); err != nil {
		return nil, media.New(media.StageParse, "dash", "representation %q has invalid bandwidth %q", rep.ID, rep.Bandwidth).WithPath(path)
	}
	if rep.AverageBandwidth != "" {
		if r.AverageBandwidth, err = parsePositiveInt64(rep.AverageBandwidth); err != nil {
			return nil, media.New(media.StageParse, "dash", "representation %q has invalid averageBandwidth %q", rep.ID, rep.AverageBandwidth).WithPath(path)
		}
	}
	if rep.Width != "" {
		if r.Width, err = parseInt(rep.Width); err != nil {
			return nil, media.New(media.StageParse, "dash", "representation %q has invalid width %q", rep.ID, rep.Width).WithPath(path)
		}
	}
	if rep.Height != "" {
		if r.Height, err = parseInt(rep.Height); err != nil {
			return nil, media.New(media.StageParse, "dash", "representation %q has invalid height %q", rep.ID, rep.Height).WithPath(path)
		}
	}
	if rep.AudioSamplingRate != "" {
		if r.AudioSamplingRate, err = parseInt(rep.AudioSamplingRate); err != nil {
			return nil, media.New(media.StageParse, "dash", "representation %q has invalid audioSamplingRate %q", rep.ID, rep.AudioSamplingRate).WithPath(path)
		}
	}

	tmpl := rep.SegmentTemplate
	if tmpl == nil {
		tmpl = set.SegmentTemplate
	}
	if tmpl != nil {
		if err := fillFromTemplate(r, path, tmpl, base, totalNs, limits); err != nil {
			return nil, err
		}
		return r, nil
	}
	if rep.SegmentList != nil {
		if err := fillFromList(r, path, rep.SegmentList, base, totalNs, limits); err != nil {
			return nil, err
		}
		return r, nil
	}
	return nil, media.New(media.StageParse, "dash", "representation %q uses SegmentBase or bare SegmentURL forms, which are not supported", rep.ID).WithPath(path)
}

func fillFromTemplate(r *Representation, path string, t *tmplXML, base string, totalNs int64, limits Limits) *media.Error {
	ts := uint32(1)
	if t.Timescale != "" {
		n, err := parsePositiveInt64(t.Timescale)
		if err != nil || n > int64(media.MaxSupportedTimescale) {
			return media.New(media.StageParse, "dash", "representation %q has invalid timescale %q", r.ID, t.Timescale).WithPath(path)
		}
		ts = uint32(n)
	}
	if t.Timescale == "0" {
		return media.New(media.StageParse, "dash", "representation %q has zero timescale", r.ID).WithPath(path)
	}
	r.Timescale = ts
	tb, err := media.NewTimebase(ts, path)
	if err != nil {
		return media.Wrap(media.StageParse, "dash", err, "representation %q", r.ID)
	}
	if t.Initialization != "" {
		r.InitURI = expandTemplate(t.Initialization, base, map[string]string{
			"$RepresentationID$": r.ID,
			"$Bandwidth$":        strconv.FormatInt(r.Bandwidth, 10),
		})
	}
	if t.Media == "" {
		return media.New(media.StageParse, "dash", "representation %q SegmentTemplate has no media attribute", r.ID).WithPath(path)
	}
	startNumber := int64(1)
	if t.StartNumber != "" {
		n, err := parseNonNegativeInt(t.StartNumber)
		if err != nil {
			return media.New(media.StageParse, "dash", "representation %q has invalid startNumber %q", r.ID, t.StartNumber).WithPath(path)
		}
		startNumber = n
	}
	var times []int64 // durations in ticks
	if t.Timeline != nil && len(t.Timeline.Items) > 0 {
		var tCursor int64
		for ii, item := range t.Timeline.Items {
			var d int64
			if item.D == "" {
				return media.New(media.StageParse, "dash", "SegmentTimeline S[%d] of representation %q has no duration", ii, r.ID).WithPath(path)
			}
			d, err := parsePositiveInt64(item.D)
			if err != nil {
				return media.New(media.StageParse, "dash", "SegmentTimeline S[%d] of representation %q has invalid d=%q", ii, r.ID, item.D).WithPath(path)
			}
			if item.T != "" {
				t, err := parseNonNegativeInt(item.T)
				if err != nil {
					return media.New(media.StageParse, "dash", "SegmentTimeline S[%d] of representation %q has invalid t=%q", ii, r.ID, item.T).WithPath(path)
				}
				if t < tCursor {
					return media.New(media.StageParse, "dash", "SegmentTimeline S[%d] of representation %q goes backwards", ii, r.ID).WithPath(path)
				}
				tCursor = t
			}
			repeat := int64(0)
			if item.R != "" {
				repeat, err = strconv.ParseInt(item.R, 10, 64)
				if err != nil || repeat < 0 {
					return media.New(media.StageParse, "dash", "SegmentTimeline S[%d] of representation %q has invalid r=%q", ii, r.ID, item.R).WithPath(path)
				}
				if repeat > 1_000_000 {
					return media.New(media.StageParse, "dash", "SegmentTimeline S[%d] of representation %q repeats too many times", ii, r.ID).WithPath(path)
				}
			}
			for k := int64(0); k <= repeat; k++ {
				if len(times) >= limits.MaxSegments {
					return media.New(media.StageParse, "dash", "segment count exceeds limit %d", limits.MaxSegments).WithPath(path)
				}
				times = append(times, d)
				tCursor += d
			}
		}
	} else {
		if t.Duration == "" {
			return media.New(media.StageParse, "dash", "representation %q SegmentTemplate needs duration or SegmentTimeline", r.ID).WithPath(path)
		}
		d, err := parsePositiveInt64(t.Duration)
		if err != nil {
			return media.New(media.StageParse, "dash", "representation %q has invalid segment duration %q", r.ID, t.Duration).WithPath(path)
		}
		if totalNs < 0 {
			return media.New(media.StageParse, "dash", "representation %q uses fixed-duration segments but the MPD declares no mediaPresentationDuration", r.ID).WithPath(path)
		}
		dNs, err := tb.ToNanos(d)
		if err != nil {
			return media.Wrap(media.StageParse, "dash", err, "representation %q", r.ID)
		}
		count := totalNs / dNs
		if totalNs%dNs != 0 {
			count++
		}
		if count <= 0 || count > int64(limits.MaxSegments) {
			return media.New(media.StageParse, "dash", "representation %q segment count %d is out of range", r.ID, count).WithPath(path)
		}
		times = make([]int64, 0, count)
		for i := int64(0); i < count; i++ {
			times = append(times, d)
		}
	}
	var cursor int64
	for i, ticks := range times {
		dNs, err := tb.ToNanos(ticks)
		if err != nil {
			return media.Wrap(media.StageParse, "dash", err, "representation %q segment %d", r.ID, i).WithSegment(i)
		}
		params := map[string]string{
			"$RepresentationID$": r.ID,
			"$Number$":           strconv.FormatInt(startNumber+int64(i), 10),
			"$Time$":             strconv.FormatInt(cursor, 10),
			"$Bandwidth$":        strconv.FormatInt(r.Bandwidth, 10),
		}
		uri := expandTemplate(t.Media, base, params)
		if !isLocalRef(uri) {
			return media.New(media.StageParse, "dash", "representation %q references remote media %q", r.ID, uri).WithPath(path).WithSegment(i)
		}
		segs, end, aerr := appendSegment(r.Segments, uri, 0, r.TotalNs, dNs, startNumber+int64(i), nil, false)
		if aerr != nil {
			return aerr.WithPath(path)
		}
		r.Segments = segs
		r.TotalNs = end
		cursor += ticks
	}
	if totalNs >= 0 {
		if err := media.CheckDrift(totalNs, r.TotalNs, "dash"); err != nil {
			return media.Wrap(media.StageParse, "dash", err, "representation %q declared duration", r.ID).WithPath(path)
		}
	}
	return nil
}

func fillFromList(r *Representation, path string, l *listXML, base string, totalNs int64, limits Limits) *media.Error {
	ts := uint32(1)
	if l.Timescale != "" {
		n, err := parsePositiveInt64(l.Timescale)
		if err != nil || n > int64(media.MaxSupportedTimescale) {
			return media.New(media.StageParse, "dash", "representation %q has invalid timescale %q", r.ID, l.Timescale).WithPath(path)
		}
		ts = uint32(n)
	}
	r.Timescale = ts
	tb, err := media.NewTimebase(ts, path)
	if err != nil {
		return media.Wrap(media.StageParse, "dash", err, "representation %q", r.ID)
	}
	if l.Initialization != nil && l.Initialization.SourceURL != "" {
		r.InitURI = joinLocal(base, l.Initialization.SourceURL)
	}
	if l.Duration == "" {
		return media.New(media.StageParse, "dash", "representation %q SegmentList has no duration", r.ID).WithPath(path)
	}
	d, err := parsePositiveInt64(l.Duration)
	if err != nil {
		return media.New(media.StageParse, "dash", "representation %q has invalid segment duration %q", r.ID, l.Duration).WithPath(path)
	}
	dNs, err := tb.ToNanos(d)
	if err != nil {
		return media.Wrap(media.StageParse, "dash", err, "representation %q", r.ID)
	}
	if len(l.SegmentURLs) == 0 {
		return media.New(media.StageParse, "dash", "representation %q SegmentList has no SegmentURL", r.ID).WithPath(path)
	}
	if len(l.SegmentURLs) > limits.MaxSegments {
		return media.New(media.StageParse, "dash", "segment count exceeds limit %d", limits.MaxSegments).WithPath(path)
	}
	startNumber := int64(1)
	if l.StartNumber != "" {
		if n, err := parseNonNegativeInt(l.StartNumber); err == nil {
			startNumber = n
		} else {
			return media.New(media.StageParse, "dash", "representation %q has invalid startNumber %q", r.ID, l.StartNumber).WithPath(path)
		}
	}
	for i, su := range l.SegmentURLs {
		if su.Media == "" {
			return media.New(media.StageParse, "dash", "representation %q SegmentURL[%d] has no media", r.ID, i).WithPath(path).WithSegment(i)
		}
		uri := joinLocal(base, su.Media)
		if !isLocalRef(uri) {
			return media.New(media.StageParse, "dash", "representation %q references remote media %q", r.ID, uri).WithPath(path).WithSegment(i)
		}
		segs, end, aerr := appendSegment(r.Segments, uri, 0, r.TotalNs, dNs, startNumber+int64(i), nil, false)
		if aerr != nil {
			return aerr.WithPath(path).WithSegment(i)
		}
		r.Segments = segs
		r.TotalNs = end
	}
	if totalNs >= 0 {
		if err := media.CheckDrift(totalNs, r.TotalNs, "dash"); err != nil {
			return media.Wrap(media.StageParse, "dash", err, "representation %q declared duration", r.ID).WithPath(path)
		}
	}
	return nil
}

func expandTemplate(t, base string, params map[string]string) string {
	s := t
	for k, v := range params {
		s = strings.ReplaceAll(s, k, v)
	}
	return joinLocal(base, s)
}

func joinLocal(base, ref string) string {
	base = strings.TrimSpace(base)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return base
	}
	if base == "" || strings.Contains(ref, "://") {
		return ref
	}
	return strings.TrimSuffix(base, "/") + "/" + ref
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func parseInt(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return n, nil
}

func parsePositiveInt64(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("not a positive integer")
	}
	return n, nil
}

// parseISODurationNanos parses xs:duration limited to day/time components,
// e.g. PT12.5S or PT1M30S. Years and months are rejected because they have no
// fixed length in integer nanoseconds. Every component is converted with the
// exact decimal-to-nanosecond parser, never floating point.
func parseISODurationNanos(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "P") || strings.ContainsAny(s, "YM") {
		return 0, fmt.Errorf("unsupported duration")
	}
	body := s[1:]
	var total int64
	if i := strings.IndexByte(body, 'T'); i >= 0 {
		date, clock := body[:i], body[i+1:]
		if date != "" {
			if !strings.HasSuffix(date, "D") {
				return 0, fmt.Errorf("unsupported duration")
			}
			ns, err := parseDecimalSeconds(strings.TrimSuffix(date, "D"), 86400)
			if err != nil {
				return 0, err
			}
			if total, err = addNanos(total, ns); err != nil {
				return 0, err
			}
		}
		body = clock
	}
	for body != "" {
		idx := strings.IndexAny(body, "HMS")
		if idx <= 0 {
			return 0, fmt.Errorf("unsupported duration")
		}
		mult := int64(1)
		switch body[idx] {
		case 'H':
			mult = 3600
		case 'M':
			mult = 60
		case 'S':
			mult = 1
		}
		ns, err := parseDecimalSeconds(body[:idx], mult)
		if err != nil {
			return 0, err
		}
		if total, err = addNanos(total, ns); err != nil {
			return 0, err
		}
		body = body[idx+1:]
	}
	return total, nil
}

// parseDecimalSeconds converts an unsigned decimal value in seconds,
// multiplied by mult, into integer nanoseconds rounded half up.
func parseDecimalSeconds(value string, mult int64) (int64, error) {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return 0, fmt.Errorf("unsupported duration")
	}
	whole, frac, hasFrac := strings.Cut(value, ".")
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w < 0 {
		return 0, fmt.Errorf("unsupported duration")
	}
	if hasFrac && len(frac) > 9 {
		return 0, fmt.Errorf("duration precision beyond nanoseconds")
	}
	var digits int64
	for _, c := range frac {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("unsupported duration")
		}
		digits = digits*10 + int64(c-'0')
	}
	for i := len(frac); i < 9; i++ {
		digits *= 10
	}
	if mult > 1 && digits != 0 {
		return 0, fmt.Errorf("fractional minutes or hours are not supported")
	}
	if w > (1<<62)/(media.NanosPerSecond*mult) {
		return 0, fmt.Errorf("duration out of range")
	}
	base := w * media.NanosPerSecond * mult
	return addNanos(base, digits)
}

func addNanos(a, b int64) (int64, error) {
	if a > (1<<63-1)-b {
		return 0, fmt.Errorf("duration out of range")
	}
	return a + b, nil
}

// offsetOfXMLError returns the byte offset reported by encoding/xml, if any.
func offsetOfXMLError(data []byte, err error) int64 {
	if err == nil || err == io.EOF {
		return -1
	}
	var se *xml.SyntaxError
	if !asSyntaxError(err, &se) {
		return -1
	}
	// encoding/xml does not expose offsets; locate the offending line text.
	return -1
}

func asSyntaxError(err error, dst **xml.SyntaxError) bool {
	se, ok := err.(*xml.SyntaxError)
	if ok {
		*dst = se
	}
	return ok
}
