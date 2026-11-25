// Package dash parses DASH Media Presentation Documents into normalized
// representations and segment references. Manifest positions are tracked
// as raw byte offsets so every structural error can be traced back to the
// exact input byte, and segment constraints (timescale, time range,
// duplicate and out-of-range references) are validated here.
package dash

import (
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"mediascan/internal/errs"
)

// SegmentRef is one media segment reference inside a representation.
type SegmentRef struct {
	Index      int    // 0-based position in the representation
	URL        string // resolved media URL (template-expanded or SegmentTemplate BaseURL)
	StartTime  int64  // t attribute, in SegmentTemplate timescale units
	Duration   int64  // d attribute
	ByteOffset int64  // source byte offset of the element, for error locators
	ByteRange  *Range // optional SegmentBase/SegmentList byte range
}

// Range is a half-open byte interval [Start, End).
type Range struct {
	Start int64
	End   int64
}

// Representation is one DASH adaptation-layer representation.
type Representation struct {
	ID        string
	MimeType  string
	Timescale uint32
	// Segments in manifest order; StartTime values are on Timescale.
	Segments []SegmentRef
	// Range spans the first segment start through the last segment end.
	Range clockRange
}

type clockRange struct {
	start int64
	end   int64
}

// Manifest is the parsed and validated MPD.
type Manifest struct {
	Source          string
	MinBufferTimeMS int64
	Representations []*Representation
	// raw content hash is computed by the fingerprint package later;
	// the manifest keeps its own bytes for offset resolution.
	raw []byte
}

// Raw returns the exact MPD bytes.
func (m *Manifest) Raw() []byte { return m.raw }

// xmlName / xmlAttr are minimal decoded tokens we retain with the byte
// offset at which the element or attribute started.
type xmlName struct {
	local string
}

type lineIndex struct {
	raw       []byte
	lineStart []int // byte offset of the start of each 0-based line
}

func buildLineIndex(raw []byte) *lineIndex {
	starts := []int{0}
	for i, b := range raw {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &lineIndex{raw: raw, lineStart: starts}
}

// offset converts a decoder line/column (1-based) into a raw byte offset.
func (li *lineIndex) offset(line, col int) int64 {
	if line < 1 || line > len(li.lineStart) {
		return errs.Unknown
	}
	off := li.lineStart[line-1] + (col - 1)
	if off > len(li.raw) {
		return errs.Unknown
	}
	return int64(off)
}

type attr struct {
	local string
	value string
}

type element struct {
	name     string
	attrs    []attr
	children []*element
	text     string
	offset   int64
}

func (e *element) attr(name string) (string, bool) {
	for _, a := range e.attrs {
		if a.local == name {
			return a.value, true
		}
	}
	return "", false
}

func (e *element) child(name string) *element {
	for _, c := range e.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (e *element) childrenAll(name string) []*element {
	var out []*element
	for _, c := range e.children {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func parseTree(raw []byte, source string) (*element, error) {
	li := buildLineIndex(raw)
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var root *element
	var stack []*element
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			line, col := dec.InputPos()
			return nil, errs.New(errs.KindManifest, source, li.offset(line, col),
				errs.Unknown, 0, "MPD is not well-formed XML: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			line, col := dec.InputPos()
			el := &element{
				name:   t.Name.Local,
				offset: li.offset(line, col),
			}
			for _, a := range t.Attr {
				el.attrs = append(el.attrs, attr{local: a.Name.Local, value: a.Value})
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, el)
			} else {
				root = el
			}
			stack = append(stack, el)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errs.New(errs.KindManifest, source, 0, errs.Unknown, 0, "empty MPD document")
	}
	if root.name != "MPD" {
		return nil, errs.New(errs.KindManifest, source, root.offset, errs.Unknown, 0,
			"root element is %q, want MPD", root.name)
	}
	return root, nil
}

func parseUint32(el *element, name string, source string, def uint32) (uint32, error) {
	v, ok := el.attr(name)
	if !ok {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil || n == 0 {
		return 0, errs.New(errs.KindManifest, source, el.offset, errs.Unknown, 0,
			"attribute %q=%q is not a positive integer", name, v)
	}
	return uint32(n), nil
}

func parseAttrInt64(el *element, name string) (int64, bool) {
	v, ok := el.attr(name)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parseByteRange(el *element, source string) (*Range, error) {
	v, ok := el.attr("mediaRange")
	if !ok {
		return nil, nil
	}
	parts := strings.Split(v, "-")
	if len(parts) != 2 {
		return nil, errs.New(errs.KindManifest, source, el.offset, errs.Unknown, 0,
			"invalid mediaRange %q, want start-end", v)
	}
	start, err1 := strconv.ParseInt(parts[0], 10, 64)
	end, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || start < 0 || end < start {
		return nil, errs.New(errs.KindManifest, source, el.offset, errs.Unknown, 0,
			"invalid mediaRange %q", v)
	}
	return &Range{Start: start, End: end + 1}, nil
}
