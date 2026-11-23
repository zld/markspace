package ebml

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// TrackType mirrors the Matroska TrackType enumeration.
type TrackType uint8

const (
	TrackVideo    TrackType = 1
	TrackAudio    TrackType = 2
	TrackSubtitle TrackType = 17
)

// Track is the subset of TrackEntry needed by the pipeline.
type Track struct {
	Number          uint64    `json:"number"`
	UID             uint64    `json:"uid"`
	Type            TrackType `json:"type"`
	CodecID         string    `json:"codec_id"`
	DefaultDuration uint64    `json:"default_duration_ns"`
	PixelWidth      uint64    `json:"pixel_width,omitempty"`
	PixelHeight     uint64    `json:"pixel_height,omitempty"`
	SamplingFreq    float64   `json:"sampling_frequency,omitempty"`
	Channels        uint64    `json:"channels,omitempty"`
}

// Block is one decoded block with its provenance.
type Block struct {
	Track       uint64 // track number
	Timecode    int64  // cluster timecode + relative timecode, in TimecodeScale units
	Keyframe    bool
	Invisible   bool
	Discardable bool
	Source      Span // raw bytes of the block element, including header
	PayloadOff  int64
	PayloadLen  int64
	Cluster     int64 // offset of enclosing cluster
}

// Span is a half-open byte range [Offset, Offset+Length).
type Span struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
}

// End returns the exclusive end offset.
func (s Span) End() int64 { return s.Offset + s.Length }

// Segment is the top-level Segment element span.
type Segment struct {
	Span        Span
	UnknownSize bool
}

// Doc is the parsed document skeleton produced by Parse.
type Doc struct {
	DocType       string
	DocTypeVer    uint64
	TimecodeScale uint64 // nanoseconds per tick
	Duration      float64
	Tracks        []Track
	Segment       Segment
	ElementCount  int
	Digest        [32]byte
	Size          int64
}

// ErrorKind classifies structural parse failures so callers can map them to
// exit codes and machine-readable diagnostics.
type ErrorKind string

const (
	ErrKindTruncated    ErrorKind = "truncated_element"
	ErrKindInvalidVINT  ErrorKind = "invalid_vint"
	ErrKindInvalidBlock ErrorKind = "invalid_block"
	ErrKindLimit        ErrorKind = "limit_exceeded"
	ErrKindIO           ErrorKind = "io_error"
	ErrKindStructure    ErrorKind = "invalid_structure"
)

// ParseError carries the location and classification of a parse failure.
type ParseError struct {
	Kind    ErrorKind `json:"kind"`
	Offset  int64     `json:"offset"`
	Track   uint64    `json:"track,omitempty"`
	Element uint32    `json:"element"`
	Message string    `json:"message"`
	Wrapped error     `json:"-"`
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("ebml %s at offset %d (element 0x%x, track %d): %s",
		e.Kind, e.Offset, e.Element, e.Track, e.Message)
}

func (e *ParseError) Unwrap() error { return e.Wrapped }

func parseError(kind ErrorKind, off int64, elem uint32, track uint64, msg string, wrapped error) *ParseError {
	return &ParseError{Kind: kind, Offset: off, Element: elem, Track: track, Message: msg, Wrapped: wrapped}
}

// BlockSink receives every block in document order. Returning an error stops
// parsing and is propagated unchanged.
type BlockSink func(Block) error

// ParseOptions bounds memory use.
type ParseOptions struct {
	// MaxMetadataSize bounds any single non-block element payload held in
	// memory. Blocks are streamed and never buffered wholesale.
	MaxMetadataSize int
}

// DefaultParseOptions returns safe defaults.
func DefaultParseOptions() ParseOptions {
	return ParseOptions{MaxMetadataSize: 8 << 20}
}

// Parse walks the document, calling sink for every block. It is strictly
// forward-only and holds at most one metadata payload in memory. Parse returns
// a *ParseError for structural failures; callers must treat any such error as
// fatal rather than silently dropping blocks.
func Parse(r io.Reader, opts ParseOptions, sink BlockSink) (*Doc, error) {
	if opts.MaxMetadataSize <= 0 {
		opts = DefaultParseOptions()
	}
	rd := NewReader(r)
	doc := &Doc{TimecodeScale: 1_000_000}
	p := &parser{rd: rd, doc: doc, opts: opts, sink: sink}
	if err := p.run(); err != nil {
		return nil, err
	}
	doc.ElementCount = p.elements
	doc.Digest = rd.Digest()
	doc.Size = rd.Offset()
	return doc, nil
}

type parser struct {
	rd       *Reader
	doc      *Doc
	opts     ParseOptions
	sink     BlockSink
	elements int
}

func (p *parser) next() (Header, error) { return p.rd.Next() }

func (p *parser) wrapErr(h Header, err error) error {
	if err == nil {
		return nil
	}
	var pe *ParseError
	if errors.As(err, &pe) {
		return err
	}
	kind := ErrKindIO
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		kind = ErrKindTruncated
	case errors.Is(err, ErrInvalidVINT):
		kind = ErrKindInvalidVINT
	case errors.Is(err, ErrTruncatedVINT):
		kind = ErrKindTruncated
	}
	return parseError(kind, h.Offset, h.ID, 0, err.Error(), err)
}

func (p *parser) run() error {
	h, err := p.next()
	if err != nil {
		return p.wrapErr(h, err)
	}
	if h.ID != IDEBML {
		return parseError(ErrKindStructure, h.Offset, h.ID, 0, "document does not start with EBML header", nil)
	}
	if err := p.readEBMLHeader(h); err != nil {
		return err
	}
	for {
		seg, err := p.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return p.wrapErr(seg, err)
		}
		switch seg.ID {
		case IDSegment:
			if err := p.readSegment(seg); err != nil {
				return err
			}
		default:
			if err := p.discard(seg); err != nil {
				return err
			}
		}
	}
}

func (p *parser) discard(h Header) error {
	if h.UnknownSize {
		return parseError(ErrKindStructure, h.Offset, h.ID, 0, "unknown-size element cannot be skipped", ErrUnknownSize)
	}
	p.elements++
	return p.rd.SkipPayload(h)
}

func readUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func readFloat(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	default:
		return 0
	}
}
