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

// rootIDs are the elements allowed at the document root.
var rootIDs = map[ElementID]bool{
	IDEBMLHeader: true,
	IDSegment:    true,
	IDVoid:       true,
}

// childIDs lists, for the containers this project descends into, which
// registered element IDs may appear directly inside them. It drives unknown
// length termination: an unknown length element ends at the first registered
// ID that is a legal child of an ancestor (a sibling) and is a boundary
// conflict when that ID is legal nowhere in the ancestor chain.
var childIDs = map[ElementID]map[ElementID]bool{
	IDEBMLHeader: idSet(IDEBMLVersion, IDEBMLReadVersion, IDEBMLMaxIDLength,
		IDEBMLMaxSizeLen, IDDocType, IDDocTypeVersion, IDDocTypeReadVer, IDVoid),
	IDSegment: idSet(IDSeekHead, IDInfo, IDTracks, IDCluster, IDCues,
		IDChapters, IDTags, IDAttachments, IDVoid),
	IDInfo:   idSet(IDTimecodeScale, IDMuxingApp, IDWritingApp, IDDuration, IDVoid),
	IDTracks: idSet(IDTrackEntry, IDVoid),
	IDTrackEntry: idSet(IDTrackNumber, IDTrackUID, IDTrackType, IDFlagDefault, IDName,
		IDLanguage, IDCodecID, IDAudio, IDVideo, IDVoid),
	IDAudio: idSet(IDSamplingFrequency, IDChannels, IDChannelPositions, IDBitDepth, IDVoid),
	IDVideo: idSet(IDPixelWidth, IDPixelHeight, IDColour, IDVoid),
	IDColour: idSet(IDBitsPerChannel, IDMatrixCoeffs, IDRange, IDTransferChars,
		IDPrimaries, IDVoid),
	IDCluster:    idSet(IDTimestamp, IDBlockGroup, IDSimpleBlock, IDVoid, IDCRC32),
	IDBlockGroup: idSet(IDBlock, IDBlockDuration, IDReferenceBlock, IDVoid, IDCRC32),
	IDCues:       idSet(IDCuePoint, IDVoid),
	IDSeekHead:   idSet(IDVoid),
}

func idSet(ids ...ElementID) map[ElementID]bool {
	m := make(map[ElementID]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// isChild reports whether id may appear directly inside parent. Registered
// IDs must be listed; unregistered extension elements are accepted so that
// documents using a superset of the registry still parse.
func isChild(parent, id ElementID) bool {
	if parent == 0 {
		if rootIDs[id] {
			return true
		}
		return !IsKnown(id)
	}
	if m := childIDs[parent]; m != nil && m[id] {
		return true
	}
	return !IsKnown(id)
}

// Parse decodes an EBML byte stream into a Document. It distinguishes known
// and unknown-length elements: an unknown-length element ends only at the end
// of its parent, the file, or the next legal sibling. Truncated headers,
// illegal VINTs, out-of-range sizes and boundary conflicts return
// *serr.Error with byte offset and path.
func Parse(data []byte) (*Document, error) {
	doc := &Document{Data: data, Children: map[*Element][]*Element{}}
	roots, _, err := parseRegion(data, 0, int64(len(data)), 0, nil, false, nil, doc)
	if err != nil {
		return nil, err
	}
	doc.Roots = roots
	return doc, nil
}

// parseRegion parses elements inside [start,limit). parent is the enclosing
// container ID (0 at the root). When stopAtSibling is true the region belongs
// to an unknown-length element: parsing returns at the offset of the first
// registered sibling ID, which outer records as legal in an ancestor context.
func parseRegion(data []byte, start, limit int64, parent ElementID,
	outer map[ElementID]bool, stopAtSibling bool, path []ElementID,
	doc *Document) ([]*Element, int64, *serr.Error) {
	out := make([]*Element, 0, 8)
	pos := start
	for pos < limit {
		id, _, perr := peekID(data, pos, limit, path)
		if perr != nil {
			return nil, 0, perr
		}
		if stopAtSibling && IsKnown(id) && !isChild(parent, id) {
			if outer[id] {
				return out, pos, nil
			}
			conflictPath := append(append([]ElementID(nil), path...), id)
			return nil, 0, errAt(serr.New(serr.CodeParentBoundary,
				"%s is not a legal child of %s and cannot close it", NameOf(id), NameOf(parent)),
				pos, conflictPath)
		}
		el, next, perr := parseOne(data, pos, limit, path)
		if perr != nil {
			return nil, 0, perr
		}
		if el.UnknownSize {
			if el.IsContainer() {
				kids, stop, kerr := parseUnknownBody(data, el, limit, path, doc)
				if kerr != nil {
					return nil, 0, kerr
				}
				el.PayloadEnd = stop
				doc.Children[el] = kids
				out = append(out, el)
				pos = stop
				continue
			}
			// Unknown-length scalar: it can only run to the surrounding
			// parent boundary or EOF; there is no nested boundary to find.
			el.PayloadEnd = limit
			out = append(out, el)
			pos = limit
			continue
		}
		if el.IsContainer() {
			childPath := append(append([]ElementID(nil), path...), el.ID)
			kids, _, kerr := parseRegion(data, el.PayloadOffset, el.PayloadEnd,
				el.ID, nil, false, childPath, doc)
			if kerr != nil {
				return nil, 0, kerr
			}
			doc.Children[el] = kids
		}
		out = append(out, el)
		pos = next
	}
	return out, pos, nil
}

// parseUnknownBody parses the children of an unknown-length container and
// returns the offset at which the container ends.
func parseUnknownBody(data []byte, el *Element, limit int64, path []ElementID,
	doc *Document) ([]*Element, int64, *serr.Error) {
	outer := make(map[ElementID]bool, 16)
	for id := range rootIDs {
		outer[id] = true
	}
	for _, ancestor := range path {
		for id := range childIDs[ancestor] {
			outer[id] = true
		}
	}
	childPath := append(append([]ElementID(nil), path...), el.ID)
	return parseRegion(data, el.PayloadOffset, limit, el.ID, outer, true, childPath, doc)
}

// peekID decodes and validates the element ID VINT at pos without consuming it.
func peekID(data []byte, pos, limit int64, path []ElementID) (ElementID, int, *serr.Error) {
	end := min(limit, int64(len(data)))
	if pos >= end {
		return 0, 0, errAt(serr.New(serr.CodeTruncatedHeader, "no byte for element ID"), pos, path)
	}
	dataVal, width, unknown, derr := DecodeVINT(data[pos:end])
	if derr != nil {
		return 0, 0, errAt(derr, pos, path)
	}
	onWire := reconstructOnWireID(dataVal, width)
	if unknown || !ValidEBMLID(ElementID(onWire)) {
		return 0, 0, errAt(serr.New(serr.CodeIllegalVINT, "illegal element ID 0x%X", onWire), pos, path)
	}
	return ElementID(onWire), width, nil
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
// the absolute offset just past the element (for unknown-length elements this
// is the enclosing limit; the caller finalizes the true end).
func parseOne(data []byte, pos, limit int64, parentPath []ElementID) (*Element, int64, *serr.Error) {
	end := min(limit, int64(len(data)))
	if pos >= end {
		return nil, 0, errAt(serr.New(serr.CodeTruncatedHeader, "no byte for element ID"), pos, parentPath)
	}
	idStart := pos
	dataVal, idWidth, idUnknown, derr := DecodeVINT(data[pos:end])
	if derr != nil {
		return nil, 0, errAt(derr, idStart, parentPath)
	}
	idVal := reconstructOnWireID(dataVal, idWidth)
	if idUnknown || !ValidEBMLID(ElementID(idVal)) {
		return nil, 0, errAt(serr.New(serr.CodeIllegalVINT, "illegal element ID 0x%X", idVal), idStart, parentPath)
	}
	id := ElementID(idVal)
	path := append(append([]ElementID(nil), parentPath...), id)
	pos += int64(idWidth)

	if pos >= end {
		return nil, 0, errAt(serr.New(serr.CodeTruncatedHeader, "missing size VINT for %s", NameOf(id)), idStart, path)
	}
	sizeStart := pos
	sizeVal, sizeWidth, sizeUnknown, serr2 := DecodeVINT(data[pos:end])
	if serr2 != nil {
		return nil, 0, errAt(serr2, sizeStart, path)
	}
	pos += int64(sizeWidth)

	el := &Element{
		ID:            id,
		Offset:        idStart,
		HeaderSize:    idWidth + sizeWidth,
		PayloadOffset: pos,
		PayloadEnd:    end,
	}
	if sizeUnknown {
		el.UnknownSize = true
		el.Size = -1
		return el, end, nil
	}
	remaining := end - pos
	if sizeVal > uint64(remaining) {
		return nil, 0, errAt(serr.New(serr.CodeLengthOutOfBounds,
			"element %s declares %d payload bytes, only %d remain in its parent",
			NameOf(id), sizeVal, remaining), idStart, path)
	}
	el.Size = int64(sizeVal)
	el.PayloadEnd = pos + int64(sizeVal)
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
