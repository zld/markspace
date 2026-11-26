package ebml

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"assetcheck/internal/media"
)

const (
	TrackTypeVideo    = 1
	TrackTypeAudio    = 2
	TrackTypeSubtitle = 17
	TrackTypeEffect   = 0x20
)

type Options struct {
	MaxDepth       int
	MaxElementSize int64
	HandleEvent    func(media.TimeEvent) error
}

func (o Options) withDefaults() Options {
	if o.MaxDepth <= 0 {
		o.MaxDepth = 12
	}
	if o.MaxElementSize <= 0 {
		o.MaxElementSize = 64 << 20
	}
	return o
}

type Node struct {
	ID     uint32 `json:"id"`
	Name   string `json:"name"`
	Offset int64  `json:"offset"`
}

type ElementInfo struct {
	ID      uint32 `json:"id"`
	Name    string `json:"name"`
	Offset  int64  `json:"offset"`
	Size    int64  `json:"size"`
	Unknown bool   `json:"unknown"`
}

type TrackInfo struct {
	Number            uint64  `json:"number"`
	Type              uint64  `json:"type"`
	Language          string  `json:"language,omitempty"`
	CodecID           string  `json:"codec_id,omitempty"`
	Channels          uint64  `json:"channels,omitempty"`
	BitsPerSample     uint64  `json:"bits_per_sample,omitempty"`
	SamplingFrequency float64 `json:"sampling_frequency,omitempty"`
}

type Metadata struct {
	DocType       string `json:"doc_type,omitempty"`
	TimecodeScale uint64 `json:"timecode_scale_ns"`
	MuxingApp     string `json:"muxing_app,omitempty"`
	WritingApp    string `json:"writing_app,omitempty"`
}

type Document struct {
	Metadata             Metadata      `json:"metadata"`
	Tracks               []TrackInfo   `json:"tracks"`
	Elements             []ElementInfo `json:"elements"`
	StructureFingerprint string        `json:"structure_fingerprint"`
	BlockCount           int           `json:"block_count"`
}

type parser struct {
	r         *CountingReader
	opts      Options
	doc       *Document
	path      []Node
	track     *TrackInfo
	clusterTC int64
	blocks    map[uint64]int
}

// ParseFile streams an EBML/MKV file without reading the complete file into memory.
func ParseFile(path string, opts Options) (*Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, media.Corrupt("INPUT_OPEN_FAILED", "cannot open input").WithDetail(path).Wrap(err)
	}
	defer f.Close()
	return Parse(f, opts)
}

func Parse(r io.Reader, opts Options) (*Document, error) {
	opts = opts.withDefaults()
	p := &parser{
		r:      NewCountingReader(r, 0),
		opts:   opts,
		doc:    &Document{Metadata: Metadata{TimecodeScale: 1_000_000}},
		blocks: map[uint64]int{},
	}
	if err := p.parseChildren(-1, 0); err != nil {
		return nil, err
	}
	fp, err := p.fingerprint()
	if err != nil {
		return nil, err
	}
	p.doc.StructureFingerprint = fp
	return p.doc, nil
}

func (p *parser) fingerprint() (string, error) {
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(p.doc.Elements)
	_ = enc.Encode(p.doc.Metadata)
	_ = enc.Encode(p.doc.Tracks)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p *parser) pathString() string {
	if len(p.path) == 0 {
		return "/"
	}
	s := ""
	for _, n := range p.path {
		s += "/" + n.Name
	}
	return s
}

func (p *parser) decorate(err error, offset int64) error {
	if err == nil {
		return nil
	}
	if me, ok := media.AsError(err); ok {
		if me.ElementPath == "" || me.ElementPath == "/" {
			me.ElementPath = p.pathString()
		}
		if me.Offset < 0 && offset >= 0 {
			me.Offset = offset
		}
		return me
	}
	return err
}

func isContainerID(id uint32) bool {
	switch id {
	case IDEBMLHeader, IDSegment, IDInfo, IDTracks, IDTrackEntry, IDCluster, IDBlockGroup:
		return true
	default:
		return false
	}
}

func ElementName(id uint32) string {
	switch id {
	case IDEBMLHeader:
		return "EBMLHeader"
	case IDSegment:
		return "Segment"
	case IDInfo:
		return "Info"
	case IDTracks:
		return "Tracks"
	case IDTrackEntry:
		return "TrackEntry"
	case IDCluster:
		return "Cluster"
	case IDBlockGroup:
		return "BlockGroup"
	case IDSimpleBlock:
		return "SimpleBlock"
	case IDBlock:
		return "Block"
	default:
		return fmt.Sprintf("Element_0x%X", id)
	}
}

// parseChildren parses until end (absolute offset), or EOF when end is -1.
func (p *parser) parseChildren(end int64, depth int) error {
	if depth > p.opts.MaxDepth {
		return media.Corrupt("EBML_DEPTH_LIMIT", "EBML element depth exceeds limit").
			WithElement(p.pathString(), p.r.Offset())
	}
	for end < 0 || p.r.Offset() < end {
		id, _, idOffset, err := ReadElementID(p.r)
		if err != nil {
			if errors.Is(err, io.EOF) && end < 0 {
				return nil
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return p.decorate(media.Corrupt("EBML_TRUNCATED_ELEMENT", "parent ends before a complete child header"), idOffset)
			}
			return p.decorate(err, idOffset)
		}

		v, err := ReadVINT(p.r)
		if err != nil {
			node := Node{ID: id, Name: ElementName(id), Offset: idOffset}
			p.path = append(p.path, node)
			err = p.decorate(err, v.Offset)
			p.path = p.path[:len(p.path)-1]
			return err
		}

		headerEnd := p.r.Offset()
		elementEnd := int64(-1)
		size := int64(-1)
		if !v.Unknown {
			if v.Value > uint64(p.opts.MaxElementSize) {
				return media.Corrupt("EBML_LENGTH_OUT_OF_BOUNDS", "element length exceeds parser limit").
					WithElement(p.pathString()+"/"+ElementName(id), idOffset).
					WithDetail(fmt.Sprintf("size=%d limit=%d", v.Value, p.opts.MaxElementSize))
			}
			size = int64(v.Value)
			elementEnd = headerEnd + size
			if end >= 0 && elementEnd > end {
				return media.Corrupt("EBML_LENGTH_OUT_OF_BOUNDS", "element payload extends beyond legal parent boundary").
					WithElement(p.pathString()+"/"+ElementName(id), idOffset).
					WithDetail(fmt.Sprintf("element_end=%d parent_end=%d", elementEnd, end))
			}
		} else {
			// Unknown-length elements terminate at the known parent boundary,
			// or at EOF when there is no enclosing known-length parent.
			elementEnd = end
		}

		info := ElementInfo{ID: id, Name: ElementName(id), Offset: idOffset, Size: size, Unknown: v.Unknown}
		p.doc.Elements = append(p.doc.Elements, info)
		node := Node{ID: id, Name: ElementName(id), Offset: idOffset}
		p.path = append(p.path, node)

		if err := p.dispatch(id, elementEnd, v.Unknown); err != nil {
			return p.decorate(err, idOffset)
		}

		if !v.Unknown {
			off := p.r.Offset()
			if off > elementEnd {
				return media.Corrupt("EBML_LENGTH_OUT_OF_BOUNDS", "element decoder consumed past declared length").
					WithElement(p.pathString(), idOffset)
			}
			if off < elementEnd {
				if _, err := io.CopyN(io.Discard, p.r, elementEnd-off); err != nil {
					return p.decorate(media.Corrupt("EBML_TRUNCATED_PAYLOAD", "could not skip trailing element bytes"), idOffset)
				}
			}
		}
		p.path = p.path[:len(p.path)-1]
	}
	return nil
}

func (p *parser) dispatch(id uint32, end int64, unknown bool) error {
	switch id {
	case IDEBMLHeader, IDSegment, IDInfo, IDTracks, IDCluster, IDBlockGroup:
		return p.parseChildren(end, len(p.path))
	case IDTrackEntry:
		t := &TrackInfo{}
		p.track = t
		err := p.parseChildren(end, len(p.path))
		p.doc.Tracks = append(p.doc.Tracks, *t)
		p.track = nil
		return err
	case IDTimecodeScale:
		v, err := p.readUint(end)
		if err == nil {
			p.doc.Metadata.TimecodeScale = v
		}
		return err
	case IDDocType:
		s, err := p.readString(end)
		if err == nil {
			p.doc.Metadata.DocType = s
		}
		return err
	case IDMuxingApp:
		s, err := p.readString(end)
		if err == nil {
			p.doc.Metadata.MuxingApp = s
		}
		return err
	case IDWritingApp:
		s, err := p.readString(end)
		if err == nil {
			p.doc.Metadata.WritingApp = s
		}
		return err
	case IDTrackNumber:
		return p.readTrackUint(func(t *TrackInfo, v uint64) { t.Number = v }, end)
	case IDTrackType:
		return p.readTrackUint(func(t *TrackInfo, v uint64) { t.Type = v }, end)
	case IDLanguage:
		s, err := p.readString(end)
		if err == nil && p.track != nil {
			p.track.Language = s
		}
		return err
	case IDCodecID:
		s, err := p.readString(end)
		if err == nil && p.track != nil {
			p.track.CodecID = s
		}
		return err
	case IDChannels:
		return p.readTrackUint(func(t *TrackInfo, v uint64) { t.Channels = v }, end)
	case IDBitDepth:
		return p.readTrackUint(func(t *TrackInfo, v uint64) { t.BitsPerSample = v }, end)
	case IDSamplingFreq:
		f, err := p.readFloat(end)
		if err == nil && p.track != nil {
			p.track.SamplingFrequency = f
		}
		return err
	case IDTimecode:
		v, err := p.readUint(end)
		if err == nil {
			p.clusterTC = int64(v)
		}
		return err
	case IDSimpleBlock:
		return p.parseSimpleBlock(end)
	default:
		// Unknown but structurally valid EBML elements are safely skipped by
		// the caller using their declared/parent length.
		return nil
	}
}

func (p *parser) readExact(end int64) ([]byte, error) {
	start := p.r.Offset()
	if end < 0 {
		return nil, media.Corrupt("EBML_UNKNOWN_FIELD_LENGTH", "scalar field has unknown length").
			WithElement(p.pathString(), start)
	}
	n := end - start
	if n < 0 || n > p.opts.MaxElementSize {
		return nil, media.Corrupt("EBML_LENGTH_OUT_OF_BOUNDS", "scalar field length is invalid").
			WithElement(p.pathString(), start)
	}
	buf := make([]byte, n)
	if err := p.r.ReadFull(buf); err != nil {
		return nil, media.Corrupt("EBML_TRUNCATED_FIELD", "scalar field is truncated").
			WithElement(p.pathString(), start).Wrap(err)
	}
	return buf, nil
}

func (p *parser) readUint(end int64) (uint64, error) {
	buf, err := p.readExact(end)
	if err != nil {
		return 0, err
	}
	if len(buf) > 8 {
		return 0, media.Corrupt("EBML_LENGTH_OUT_OF_BOUNDS", "unsigned integer exceeds 8 bytes").
			WithElement(p.pathString(), p.r.Offset()-int64(len(buf)))
	}
	padded := make([]byte, 8)
	copy(padded[8-len(buf):], buf)
	return binary.BigEndian.Uint64(padded), nil
}

func (p *parser) readString(end int64) (string, error) {
	buf, err := p.readExact(end)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func (p *parser) readFloat(end int64) (float64, error) {
	buf, err := p.readExact(end)
	if err != nil {
		return 0, err
	}
	switch len(buf) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(buf))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(buf)), nil
	default:
		return 0, media.Corrupt("EBML_UNSUPPORTED_FLOAT_WIDTH", "only 32- and 64-bit EBML floats are supported").
			WithElement(p.pathString(), p.r.Offset()-int64(len(buf)))
	}
}

func (p *parser) readTrackUint(set func(*TrackInfo, uint64), end int64) error {
	v, err := p.readUint(end)
	if err != nil {
		return err
	}
	if p.track == nil {
		return media.Corrupt("EBML_ORPHAN_TRACK_FIELD", "track field outside TrackEntry").
			WithElement(p.pathString(), p.r.Offset())
	}
	set(p.track, v)
	return nil
}
