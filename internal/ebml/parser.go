package ebml

import (
	"tracecheck/internal/serr"
)

// Document is a parsed EBML document over an in-memory byte slice. Offsets in
// every Element are absolute indices into Data, so random access by position
// never re-parses the file.
type Document struct {
	Data     []byte
	Roots    []*Element // top level elements (EBML header, Segment, ...)
	Children map[*Element][]*Element
}

// Parse decodes an EBML byte stream into a Document. It distinguishes known
// and unknown-length elements: an unknown-length element ends only at the end
// of its parent or of the file. Truncated headers, illegal VINTs, out-of-range
// sizes and boundary conflicts return *serr.Error with byte offset and path.
func Parse(data []byte) (*Document, error) {
	doc := &Document{Data: data, Children: map[*Element][]*Element{}}
	roots, err := parseChildren(data, 0, int64(len(data)), nil, doc)
	if err != nil {
		return nil, err
	}
	doc.Roots = roots
	return doc, nil
}

// parseChildren parses elements inside the region [start,end). parentPath is
// the chain of enclosing element IDs used for structured errors.
func parseChildren(data []byte, start, end int64, parentPath []ElementID, doc *Document) ([]*Element, error) {
	var out []*Element
	pos := start
	for pos < end {
		el, next, perr := parseOne(data, pos, end, parentPath)
		if perr != nil {
			return nil, perr
		}
		out = append(out, el)
		if el.IsContainer() {
			doc.Children[el] = []*Element{}
			kids, err := parseChildren(data, el.PayloadOffset, el.PayloadEnd,
				append(parentPath, el.ID), doc)
			if err != nil {
				return nil, err
			}
			doc.Children[el] = kids
		}
		pos = next
	}
	return out, nil
}

// reconstructOnWireID packs a decoded VINT_DATA value back into its on-wire
// bytes (big endian, the given width) and re-sets the first-byte marker. This
// avoids overflow when the data value already occupies the top integer bit.
func reconstructOnWireID(dataVal uint64, width int) uint64 {
	raw := make([]byte, width)
	v := dataVal
	for i := width - 1; i >= 0; i-- {
		raw[i] = byte(v & 0xFF)
		v >>= 8
	}
	raw[0] |= byte(1 << (8 - uint(width)))
	var out uint64
	for _, b := range raw {
		out = out<<8 | uint64(b)
	}
	return out
}

// parseOne reads a single element header at pos and returns it together with
// the absolute offset just past the element.
func parseOne(data []byte, pos, end int64, parentPath []ElementID) (*Element, int64, *serr.Error) {
	path := append(append([]ElementID(nil), parentPath...), 0)
	if pos >= int64(len(data)) {
		return nil, 0, errAt(serr.New(serr.CodeTruncatedHeader, "no byte for element ID"), pos, parentPath)
	}
	idStart := pos
	dataVal, idWidth, idUnknown, derr := DecodeVINT(data[pos:])
	if derr != nil {
		return nil, 0, errAt(derr, idStart, parentPath)
	}
	idVal := reconstructOnWireID(dataVal, idWidth)
	if idUnknown || !ValidEBMLID(ElementID(idVal)) {
		return nil, 0, errAt(serr.New(serr.CodeIllegalVINT, "illegal element ID 0x%X", idVal), idStart, parentPath)
	}
	id := ElementID(idVal)
	path[len(path)-1] = id
	pos += int64(idWidth)

	if pos >= end || pos >= int64(len(data)) {
		return nil, 0, errAt(serr.New(serr.CodeTruncatedHeader, "missing size VINT for %s", NameOf(id)), idStart, path)
	}
	sizeStart := pos
	sizeVal, sizeWidth, sizeUnknown, serr2 := DecodeVINT(data[pos:])
	if serr2 != nil {
		return nil, 0, errAt(serr2, sizeStart, path)
	}
	pos += int64(sizeWidth)

	el := &Element{
		ID:            id,
		Offset:        idStart,
		HeaderSize:    idWidth + sizeWidth,
		PayloadOffset: pos,
	}
	if sizeUnknown {
		el.UnknownSize = true
		el.Size = -1
		// Unknown-length element extends to the enclosing parent/EOF boundary.
		el.PayloadEnd = end
	} else {
		el.Size = int64(sizeVal)
		el.PayloadEnd = pos + int64(sizeVal)
		if el.PayloadEnd > end || el.PayloadEnd > int64(len(data)) {
			return nil, 0, errAt(serr.New(serr.CodeLengthOutOfBounds,
				"element %s payload end %d crosses boundary %d", NameOf(id), el.PayloadEnd, end),
				idStart, path)
		}
	}
	return el, el.PayloadEnd, nil
}

// Find returns the first element matching the path from the document roots,
// or nil when absent. Each path entry matches one nesting level.
func (d *Document) Find(path ...ElementID) *Element {
	if len(path) == 0 {
		return nil
	}
	for _, r := range d.Roots {
		if r.ID == path[0] {
			if len(path) == 1 {
				return r
			}
			if e := d.findIn(r, path[1:]); e != nil {
				return e
			}
		}
	}
	return nil
}

func (d *Document) findIn(parent *Element, rest []ElementID) *Element {
	for _, c := range d.Children[parent] {
		if c.ID != rest[0] {
			continue
		}
		if len(rest) == 1 {
			return c
		}
		if e := d.findIn(c, rest[1:]); e != nil {
			return e
		}
	}
	return nil
}

// All returns every element with the given id anywhere in the document.
func (d *Document) All(id ElementID) []*Element {
	var out []*Element
	for _, parent := range d.Roots {
		d.collect(parent, id, &out)
	}
	return out
}
func (d *Document) collect(el *Element, id ElementID, out *[]*Element) {
	if el.ID == id {
		*out = append(*out, el)
	}
	for _, c := range d.Children[el] {
		d.collect(c, id, out)
	}
}

// Payload returns the raw payload bytes of el.
func (d *Document) Payload(el *Element) []byte {
	return d.Data[el.PayloadOffset:el.PayloadEnd]
}

// PathOf returns the enclosing element IDs of el (including el itself).
func (d *Document) PathOf(target *Element) []ElementID {
	var walk func(parent *Element, chain []ElementID) []ElementID
	walk = func(parent *Element, chain []ElementID) []ElementID {
		next := append(append([]ElementID(nil), chain...), parent.ID)
		if parent == target {
			return next
		}
		for _, c := range d.Children[parent] {
			if r := walk(c, next); r != nil {
				return r
			}
		}
		return nil
	}
	for _, r := range d.Roots {
		if p := walk(r, nil); p != nil {
			return p
		}
	}
	return nil
}
