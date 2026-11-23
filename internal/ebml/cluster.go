package ebml

// Block is a fully contained Matroska block recovered from a SimpleBlock or
// Block element. Blocks whose bytes were incomplete are never emitted here.
type Block struct {
	TrackNumber uint32
	// TimecodeNs is the absolute block timecode expressed in integer
	// nanoseconds on the segment timebase.
	TimecodeNs int64
	Flags      byte
	Keyframe   bool
	Data       []byte
	Offset     int64 // absolute file offset of the enclosing element
}

// Cluster is the parsed content of one Cluster element.
type Cluster struct {
	Offset       int64
	End          int64
	TimecodeMs   int64
	UnknownSize  bool
	Blocks       []Block
	Errors       []*Error
	Truncated    bool // region ended before every child completed
}

// ParseCluster parses the payload of a Cluster element. scaleNs is the
// segment TimecodeScale in nanoseconds (1,000,000 when absent). Complete
// blocks are always retained; a partially present block is recorded as an
// error and discarded rather than surfaced as a sample.
func ParseCluster(el *Element, scaleNs int64) *Cluster {
	base := NewReader(el.Payload, el.Offset+int64(el.Header))
	base.Enter(el)
	return parseClusterWith(el, scaleNs, base)
}

func parseClusterWith(el *Element, scaleNs int64, r *Reader) *Cluster {
	if scaleNs <= 0 {
		scaleNs = 1_000_000
	}
	c := &Cluster{Offset: el.Offset, End: el.End, UnknownSize: el.Size == UnknownSize}
	for !r.Done() {
		child, err := r.Next()
		if err != nil {
			c.Truncated = true
			c.Errors = append(c.Errors, classify(err, IDCluster, r.Pos()))
			// Confirmed boundary: stop here, keep everything parsed so far.
			break
		}
		c.consumeChild(child, scaleNs)
	}
	return c
}

// classify normalizes any parser error into a structured *Error tagged with
// the enclosing master element ID.
func classify(err error, context uint32, offset int64) *Error {
	if pe, ok := err.(*Error); ok {
		if pe.ContextID == 0 {
			pe.ContextID = context
		}
		return pe
	}
	if eof, ok := err.(*EOFAt); ok {
		return &Error{Offset: eof.Offset, Category: CategoryTruncated,
			ContextID: context, Message: "region ended before element completed"}
	}
	return &Error{Offset: offset, Category: CategoryTruncated,
		ContextID: context, Message: err.Error()}
}

// consumeChild handles one fully bounded cluster child.
func (c *Cluster) consumeChild(child *Element, scaleNs int64) {
	switch child.ID {
	case IDTimecode:
		if v, ok := Uint(child); ok {
			c.TimecodeMs = int64(v)
		}
	case IDSimpleBlock, IDBlock:
		b, perr := parseBlock(child, c.TimecodeMs, scaleNs)
		if perr != nil {
			c.Errors = append(c.Errors, perr)
			return
		}
		c.Blocks = append(c.Blocks, b)
	}
}

func parseBlock(el *Element, clusterTcMs, scaleNs int64) (Block, *Error) {
	p := el.Payload
	if len(p) < 4 {
		return Block{}, &Error{Offset: el.Offset, Category: CategoryBadBlock,
			Message: "block shorter than track VINT + timecode + flags"}
	}
	track, width, err := readVINT(p, 0, int64(len(p)))
	if err != nil {
		return Block{}, &Error{Offset: el.Offset, Category: CategoryBadBlock,
			Message: "block track number VINT unreadable"}
	}
	if int(width)+2 >= len(p) {
		return Block{}, &Error{Offset: el.Offset, Category: CategoryBadBlock,
			Message: "block missing timecode or flags bytes"}
	}
	rel := int16(p[width])<<8 | int16(p[width+1])
	flags := p[width+2]
	data := p[width+3:]
	absMs := clusterTcMs + int64(rel)
	b := Block{
		TrackNumber: uint32(track),
		TimecodeNs:  absMs * scaleNs,
		Flags:       flags,
		Keyframe:    flags&0x80 != 0,
		Data:        data,
		Offset:      el.Offset,
	}
	return b, nil
}

// Document is the top-level parse result: container metadata plus every
// cluster that contained at least one parseable element.
type Document struct {
	TimecodeScaleNs int64
	DurationNs      int64
	Clusters        []*Cluster
	SegmentOffset   int64
	SegmentUnknown  bool
	Errors          []*Error
}

// ParseDocument scans a whole Matroska/WebM byte stream. It locates the
// Segment, reads Info metadata and walks its Cluster children. An
// unknown-length segment is bounded by the end of the buffer.
func ParseDocument(data []byte) *Document {
	doc := &Document{TimecodeScaleNs: 1_000_000}
	top := NewReader(data, 0)
	for !top.Done() {
		el, err := top.Next()
		if err != nil {
			doc.recordTopError(err, data, top)
			return doc
		}
		switch el.ID {
		case IDSegment:
			doc.SegmentOffset = el.Offset
			doc.SegmentUnknown = el.Size == UnknownSize
			// Push before descending so errors inside the segment carry its
			// context even when the segment itself is unknown-length.
			top.masters = append(top.masters, masterFrame{id: IDSegment, off: el.Offset})
			readSegment(doc, top, el)
			return doc
		case IDCluster:
			// Tolerate bare Cluster streams produced by damaged recorders.
			top.masters = append(top.masters, masterFrame{id: IDCluster, off: el.Offset})
			c := parseClusterWith(el, doc.TimecodeScaleNs, top.childFor(el))
			top.Leave()
			doc.addCluster(c)
			doc.Errors = append(doc.Errors, c.Errors...)
		}
	}
	if len(doc.Clusters) == 0 && len(doc.Errors) == 0 {
		doc.Errors = append(doc.Errors, &Error{
			Offset: 0, Category: CategoryTruncated, Message: "no Segment element found",
		})
	}
	return doc
}

// recordTopError handles a failure while walking the top-level stream. When
// the failing element sits inside a master (unknown-length Segment or
// Cluster recorded on the reader stack), that master is rebuilt and its
// confirmed blocks salvaged.
func (doc *Document) recordTopError(err error, data []byte, top *Reader) {
	pe, ok := err.(*Error)
	if !ok {
		doc.Errors = append(doc.Errors, classify(err, 0, 0))
		return
	}
	doc.Errors = append(doc.Errors, pe)
	if pe.ContextID == IDSegment || pe.ContextID == IDCluster {
		if master, ok := rebuildAt(data, pe.ContextOffset); ok {
			doc.salvage(master)
			return
		}
	}
	if id, off := top.LastOK(); id == IDSegment || id == IDCluster {
		if master, ok := rebuildAt(data, off); ok {
			doc.salvage(master)
			return
		}
	}
	// Explicit-size master truncated mid-stream: rebuild the master itself.
	if part, ok := PartialElement(data, 0, pe); ok {
		doc.salvage(part)
	}
}

// rebuildAt constructs an unknown-length master element directly from its
// header at off, without re-parsing (re-parsing would hit the same
// truncation). The payload runs from after the header to the end of data.
func rebuildAt(data []byte, off int64) (*Element, bool) {
	if off < 0 || off >= int64(len(data)) {
		return nil, false
	}
	id, idWidth, err := readID(data, off, int64(len(data)))
	if err != nil || idWidth == 0 {
		return nil, false
	}
	pos := off + int64(idWidth)
	_, sizeWidth, err := readVINT(data, pos, int64(len(data)))
	if err != nil {
		return nil, false
	}
	header := int64(idWidth + sizeWidth)
	return &Element{
		ID:      uint32(id),
		Offset:  off,
		Size:    UnknownSize,
		Header:  int(header),
		Payload: data[off+header:],
		End:     int64(len(data)),
	}, true
}

// addCluster appends c unless a cluster at the same offset was already
// registered by the in-place walk. Salvage paths must not create duplicates.
func (doc *Document) addCluster(c *Cluster) {
	for _, existing := range doc.Clusters {
		if existing.Offset == c.Offset {
			return
		}
	}
	doc.Clusters = append(doc.Clusters, c)
}

// salvage processes a partial master element recovered at a truncation
// point; only Cluster payloads carry salvageable media blocks.
func (doc *Document) salvage(el *Element) {
	switch el.ID {
	case IDSegment:
		doc.SegmentOffset = el.Offset
		doc.SegmentUnknown = true
		top := NewReader(dataOf(el), el.Offset)
		readSegment(doc, top, el)
	case IDCluster:
		c := ParseCluster(el, doc.TimecodeScaleNs)
		if len(c.Blocks) > 0 || c.TimecodeMs != 0 || len(c.Errors) > 0 {
			doc.addCluster(c)
			doc.Errors = append(doc.Errors, c.Errors...)
		}
	}
}

func dataOf(el *Element) []byte { return el.Payload }

func readSegment(doc *Document, top *Reader, seg *Element) {
	r := top.childFor(seg)
	for !r.Done() {
		el, err := r.Next()
		if err != nil {
			doc.recordSegmentError(err, seg, r)
			return
		}
		switch el.ID {
		case IDInfo:
			readInfo(doc, r, el)
		case IDCluster:
			c := parseClusterWith(el, doc.TimecodeScaleNs, r.childFor(el))
			doc.addCluster(c)
			doc.Errors = append(doc.Errors, c.Errors...)
		}
	}
}

// recordSegmentError mirrors recordTopError inside a Segment payload: an
// unknown-length Cluster cut at the tail is rebuilt from its recorded offset.
func (doc *Document) recordSegmentError(err error, seg *Element, r *Reader) {
	pe, ok := err.(*Error)
	if !ok {
		doc.Errors = append(doc.Errors, classify(err, IDSegment, seg.Offset))
		return
	}
	doc.Errors = append(doc.Errors, pe)
	base := seg.Offset + int64(seg.Header)
	if id, absOff := r.LastOK(); id == IDCluster {
		rel := absOff - base
		if rel >= 0 && rel < int64(len(seg.Payload)) {
			if master, ok := rebuildAt(seg.Payload, rel); ok {
				doc.salvage(master)
			}
		}
	}
}

func readInfo(doc *Document, parent *Reader, info *Element) {
	r := ChildReaderFor(parent, info)
	for !r.Done() {
		el, err := r.Next()
		if err != nil {
			doc.Errors = append(doc.Errors, classify(err, IDInfo, r.Pos()))
			return
		}
		switch el.ID {
		case IDTimecodeScale:
			if v, ok := Uint(el); ok && v > 0 {
				doc.TimecodeScaleNs = int64(v)
			}
		case IDDuration:
			if f, ok := Float(el); ok {
				doc.DurationNs = int64(f * float64(doc.TimecodeScaleNs))
			}
		}
	}
}
