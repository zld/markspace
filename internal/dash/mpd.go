// Package dash parses the subset of MPEG-DASH needed to build a reviewable
// segment manifest: periods, adaptation sets, representations and
// SegmentTemplate indexes. Element diagnostics carry the byte offset of the
// MPD element that caused them.
package dash

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"mediadeliver/internal/model"
)

// MPD is the parsed presentation.
type MPD struct {
	Path       string            `json:"path"`
	Type       string            `json:"type,omitempty"`
	DurationNS int64             `json:"duration_ns"`
	Periods    []Period          `json:"periods"`
	Errors     []*model.Error    `json:"errors,omitempty"`
	Reps       []*Representation `json:"-"`
	// Truncated marks a document that ended before its final closing tag.
	// The parser still returns the periods it saw so callers can report a
	// precise offset instead of discarding the whole index.
	Truncated bool `json:"truncated,omitempty"`
}

// Period is a time-bounded presentation interval.
type Period struct {
	ID      string            `json:"id,omitempty"`
	StartNS int64             `json:"start_ns"`
	DurNS   int64             `json:"duration_ns"`
	Reps    []*Representation `json:"-"`
}

// Representation is one encodable variant.
type Representation struct {
	ID            string         `json:"id"`
	Bandwidth     int64          `json:"bandwidth"`
	MimeType      string         `json:"mime_type,omitempty"`
	Codecs        string         `json:"codecs,omitempty"`
	ContentType   string         `json:"content_type,omitempty"`
	PeriodID      string         `json:"period_id,omitempty"`
	PathRef       string         `json:"path,omitempty"`
	PeriodStartNS int64          `json:"period_start_ns"`
	Timescale     uint32         `json:"timescale"`
	Template      *Template      `json:"template,omitempty"`
	Segments      []Segment      `json:"segments"`
	Errors        []*model.Error `json:"errors,omitempty"`
}

// Template is a SegmentTemplate merged across inheritance levels.
type Template struct {
	Timescale       uint32
	TimescaleOffset int64
	Duration        uint64 // ticks, zero when absent
	StartNumber     uint64
	Media           string
	Initialization  string
	PTO             uint64 // presentationTimeOffset in ticks
	Timeline        []TimelineEntry
	HasTimeline     bool
	Offset          int64
	Errors          []*model.Error
}

// TimelineEntry is one S element; Repeat is the raw repeat count.
type TimelineEntry struct {
	TimeTicks uint64
	DurTicks  uint64
	Repeat    int64
	Offset    int64
}

// Segment is one resolved media segment.
type Segment struct {
	Number           uint64 `json:"number"`
	TimeTicks        uint64 `json:"time_ticks"`
	DurationTicks    uint64 `json:"duration_ticks"`
	StartNS          int64  `json:"start_ns"`
	DurationNS       int64  `json:"duration_ns"`
	URI              string `json:"uri"`
	SourceOffset     int64  `json:"source_offset"`
	RepresentationID string `json:"representation_id"`
	Timescale        uint32 `json:"timescale"`
}

// SourceOffsetOrZero is the offset of the first representation diagnostic.
func (r *Representation) SourceOffsetOrZero() int64 {
	if r != nil && len(r.Errors) > 0 {
		return r.Errors[0].Offset
	}
	return 0
}

// StartEnd returns the nanosecond interval of the segment.
func (s Segment) StartEnd() (int64, int64) { return s.StartNS, s.StartNS + s.DurationNS }

type attr struct {
	value  string
	offset int64
}

type rawElement struct {
	name     string
	attrs    map[string]attr
	offset   int64
	children []*rawElement
}

// ParseFile reads and parses an MPD from disk.
func ParseFile(path string) (*MPD, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.Wrap(err, model.CodeMPDInvalid, path, model.NoOffset, model.StageParse, false)
	}
	return Parse(data, path)
}

// Parse parses MPD bytes. A nil error means the document structure was
// readable; element-level problems are reported in MPD.Errors.
func Parse(data []byte, path string) (*MPD, error) {
	root, truncatedAt, err := buildTree(data, path)
	if err != nil {
		return nil, err
	}
	if root == nil || root.name != "MPD" {
		return nil, model.NewError(model.CodeMPDInvalid, path, 0, model.StageParse, false,
			"document root is not MPD")
	}
	mp := &MPD{Path: path, Type: attrString(root, "type")}
	if truncatedAt >= 0 {
		mp.Truncated = true
		mp.Errors = append(mp.Errors, model.NewError(model.CodeMPDTruncated, path, truncatedAt,
			model.StageParse, true, "MPD ended before its final closing tag"))
	}
	mp.DurationNS = parseDurationAttr(root, "mediaPresentationDuration", path, &mp.Errors)
	for _, pel := range childElements(root, "Period") {
		p := parsePeriod(pel, path, mp)
		mp.Periods = append(mp.Periods, p)
		mp.Reps = append(mp.Reps, p.Reps...)
	}

	if len(mp.Periods) == 0 {
		mp.Errors = append(mp.Errors, model.NewError(model.CodeMPDInvalid, path, root.offset,
			model.StageParse, false, "MPD contains no Period"))
	}
	model.SortErrors(mp.Errors)
	return mp, nil
}

// buildTree turns the XML token stream into a lightweight tree while
// recording the exact byte offset of every start tag.
// buildTree parses the document into a raw element tree. A truncated
// document returns the tree parsed so far plus the offset at which input
// stopped, so the caller can still emit locatable diagnostics.
func buildTree(data []byte, path string) (*rawElement, int64, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	searchFrom := 0
	var root *rawElement
	var stack []*rawElement
	truncatedAt := int64(-1)
	for {
		before := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if root != nil && strings.Contains(err.Error(), "EOF") && len(stack) > 0 {
				// Keep the partially parsed tree; Parse records the
				// truncation with the byte offset where input stopped.
				truncatedAt = int64(len(data))
				goto done
			}
			code := model.CodeMPDInvalid
			off := before
			if strings.Contains(err.Error(), "EOF") {
				code = model.CodeMPDTruncated
				off = int64(len(data))
			}
			return nil, -1, model.NewError(code, path, off, model.StageParse, false, err.Error())
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			if _, end := tok.(xml.EndElement); end && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		off, found := findStartTag(data, searchFrom, start.Name.Local)
		if !found {
			off = int(before)
		}
		searchFrom = off + 1
		el := &rawElement{name: start.Name.Local, offset: int64(off), attrs: map[string]attr{}}
		for _, a := range start.Attr {
			el.attrs[a.Name.Local] = attr{value: a.Value, offset: attrOffsetIn(data, off, a.Name.Local)}
		}
		if len(stack) == 0 {
			if root == nil {
				root = el
			}
		} else {
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, el)
		}
		stack = append(stack, el)
	}
done:
	return root, truncatedAt, nil
}

// attrOffsetIn returns the byte offset of the attribute name inside the
// start tag beginning at tagStart. Encoding/xml hands back decoded values, so
// the raw buffer is the only place the original offset can be recovered.
func attrOffsetIn(data []byte, tagStart int, name string) int64 {
	if tagStart < 0 || tagStart >= len(data) {
		return int64(tagStart)
	}
	limit := len(data)
	if end := bytes.IndexByte(data[tagStart:], '>'); end >= 0 {
		limit = tagStart + end
	}
	needle := []byte(name)
	for i := tagStart; i < limit; i++ {
		if !bytes.HasPrefix(data[i:], needle) {
			continue
		}
		if i == tagStart+1 { // element name itself, not an attribute
			continue
		}
		j := i + len(needle)
		for j < limit && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
			j++
		}
		if j < limit && data[j] == '=' {
			return int64(i)
		}
	}
	return int64(tagStart)
}

func findStartTag(data []byte, from int, name string) (int, bool) {
	needle := []byte("<" + name)
	if from >= len(data) {
		return 0, false
	}
	idx := bytes.Index(data[from:], needle)
	if idx < 0 {
		return 0, false
	}
	return from + idx, true
}

func childElements(el *rawElement, name string) []*rawElement {
	var out []*rawElement
	for _, c := range el.children {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func attrString(el *rawElement, name string) string { return el.attrs[name].value }
func attrOffset(el *rawElement, name string) int64 {
	if a, ok := el.attrs[name]; ok {
		return a.offset
	}
	return el.offset
}

func attrInt(el *rawElement, name string) (int64, bool, *model.Error) {
	a, ok := el.attrs[name]
	if !ok || a.value == "" {
		return 0, false, nil
	}
	v, err := strconv.ParseInt(a.value, 10, 64)
	if err != nil {
		return 0, true, model.NewError(model.CodeMPDInvalid, "", a.offset, model.StageParse, false,
			fmt.Sprintf("attribute %s=%q is not an integer", name, a.value))
	}
	return v, true, nil
}

func attrUint(el *rawElement, name string) (uint64, bool, *model.Error) {
	v, ok, err := attrInt(el, name)
	if err != nil || !ok {
		return 0, ok, err
	}
	if v < 0 {
		return 0, true, model.NewError(model.CodeNegativeDuration, "", attrOffset(el, name),
			model.StageParse, false, fmt.Sprintf("attribute %s is negative", name))
	}
	return uint64(v), true, nil
}

func parseDurationAttr(el *rawElement, name, path string, errs *[]*model.Error) int64 {
	a, ok := el.attrs[name]
	if !ok || a.value == "" {
		return 0
	}
	ns, err := model.ParseISODuration(a.value)
	if err != nil {
		*errs = append(*errs, model.Wrap(err, model.CodeMPDInvalid, path, a.offset, model.StageParse, false))
		return 0
	}
	return ns
}
