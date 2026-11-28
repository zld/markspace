package mediakernel

import (
	"fmt"
	"io"
	"math"
)

// UnknownSize marks an element whose size VINT is all-ones (EBML unknown size).
const UnknownSize = int64(-1)

// Matroska/EBML element IDs used by this kernel.
const (
	IDEBML               = 0x1A45DFA3
	IDSegment            = 0x18538067
	IDSeekHead           = 0x114D9B74
	IDInfo               = 0x1549A966
	IDTracks             = 0x1654AE6B
	IDCluster            = 0x1F43B675
	IDCues               = 0x1C53BB6B
	IDVoid               = 0xEC
	IDCRC32              = 0xBF
	IDTimecodeScale      = 0x2AD7B1
	IDDuration           = 0x4489
	IDTrackEntry         = 0xAE
	IDTrackNumber        = 0xD7
	IDTrackUID           = 0x73C5
	IDTrackType          = 0x83
	IDCodecID            = 0x86
	IDDefaultDuration    = 0x23E383
	IDTrackTimecodeScale = 0x23314F
	IDAudio              = 0xE1
	IDVideo              = 0xE0
	IDTimecode           = 0xE7
	IDSimpleBlock        = 0xA3
	IDBlockGroup         = 0xA0
	IDBlock              = 0xA1
	IDChannels           = 0x9F
	IDSamplingFrequency  = 0xB5
)

// elemLevel gives the EBML tree level of known elements.
var elemLevel = map[uint32]int{
	IDEBML: 0, IDSegment: 1,
	IDSeekHead: 2, IDInfo: 2, IDTracks: 2, IDCluster: 2, IDCues: 2,
	IDTimecodeScale: 3, IDDuration: 3, IDTrackEntry: 3, IDTimecode: 3,
	IDSimpleBlock: 3, IDBlockGroup: 3,
	IDTrackNumber: 4, IDTrackUID: 4, IDTrackType: 4, IDCodecID: 4,
	IDDefaultDuration: 4, IDTrackTimecodeScale: 4, IDAudio: 4, IDVideo: 4, IDBlock: 4,
	IDChannels: 5, IDSamplingFrequency: 5,
}

var masterIDs = map[uint32]bool{
	IDEBML: true, IDSegment: true, IDSeekHead: true, IDInfo: true,
	IDTracks: true, IDCluster: true, IDBlockGroup: true, IDTrackEntry: true,
	IDAudio: true, IDVideo: true, IDCues: true,
}

var elemNames = map[uint32]string{
	IDEBML: "EBML", IDSegment: "Segment", IDSeekHead: "SeekHead",
	IDInfo: "Info", IDTracks: "Tracks", IDCluster: "Cluster", IDCues: "Cues",
	IDVoid: "Void", IDCRC32: "CRC32",
	IDTimecodeScale: "TimecodeScale", IDDuration: "Duration",
	IDTrackEntry: "TrackEntry", IDTrackNumber: "TrackNumber", IDTrackUID: "TrackUID",
	IDTrackType: "TrackType", IDCodecID: "CodecID",
	IDDefaultDuration: "DefaultDuration", IDTrackTimecodeScale: "TrackTimecodeScale",
	IDAudio: "Audio", IDVideo: "Video",
	IDTimecode: "Timecode", IDSimpleBlock: "SimpleBlock",
	IDBlockGroup: "BlockGroup", IDBlock: "Block",
	IDChannels: "Channels", IDSamplingFrequency: "SamplingFrequency",
}

func elemName(id uint32) string {
	if n, ok := elemNames[id]; ok {
		return n
	}
	return fmt.Sprintf("0x%X", id)
}

// Element is a parsed EBML element header.
type Element struct {
	ID           uint32
	Size         int64 // UnknownSize (-1) when the size VINT is all-ones
	HeaderOffset int64
	DataOffset   int64
	Path         string
	Master       bool
}

type openElem struct {
	id   uint32
	end  int64 // absolute end offset of element data; UnknownSize if open-ended
	path string
}

// Parser is a sequential EBML stream parser with byte-exact error reporting.
type Parser struct {
	r     io.Reader
	pos   int64
	total int64 // total input size, -1 if unknown
	stack []openElem
}

func NewParser(r io.Reader, total int64) *Parser { return &Parser{r: r, total: total} }

// Offset returns the current absolute read offset.
func (p *Parser) Offset() int64 { return p.pos }

func (p *Parser) path() string {
	s := ""
	for _, o := range p.stack {
		s += "/" + elemName(o.id)
	}
	if s == "" {
		return "/"
	}
	return s
}

func (p *Parser) readByte() (byte, error) {
	var b [1]byte
	if _, err := io.ReadFull(p.r, b[:]); err != nil {
		return 0, err
	}
	p.pos++
	return b[0], nil
}

// readVINT reads one EBML variable-length integer. With strip=false the
// marker bit is kept (element IDs); with strip=true it is removed (sizes).
// A clean EOF before the first byte returns io.EOF; EOF mid-VINT is a
// structured ErrTruncatedHeader pointing at the first missing byte.
func (p *Parser) readVINT(strip bool) (uint64, int, int64, error) {
	off := p.pos
	b0, err := p.readByte()
	if err != nil {
		return 0, 0, off, io.EOF
	}
	if b0 == 0 {
		return 0, 0, off, &Error{ErrInvalidVINT, off, p.path(), "VINT first byte is zero"}
	}
	w := 1
	for mask := byte(0x80); w < 8 && b0&mask == 0; mask >>= 1 {
		w++
	}
	val := uint64(b0)
	if strip {
		val &= uint64(0xFF >> w)
	}
	for i := 1; i < w; i++ {
		b, err := p.readByte()
		if err != nil {
			return 0, 0, off, &Error{ErrTruncatedHeader, p.pos, p.path(),
				fmt.Sprintf("VINT declares width %d but input ended after %d bytes", w, i)}
		}
		val = val<<8 | uint64(b)
	}
	return val, w, off, nil
}

// Next parses the next element header, terminating unknown-size elements only
// at a legal parent boundary or at EOF.
func (p *Parser) Next() (*Element, error) {
	// Pop every element (known or unknown size) enclosed by a known-size
	// ancestor whose end has been reached.
	cut := -1
	for i, o := range p.stack {
		if o.end >= 0 && p.pos >= o.end {
			cut = i
			break
		}
	}
	if cut >= 0 {
		p.stack = p.stack[:cut]
	}

	idOff := p.pos
	idRaw, _, _, err := p.readVINT(false)
	if err != nil {
		if err == io.EOF {
			for _, o := range p.stack {
				if o.end >= 0 && p.pos < o.end {
					return nil, &Error{ErrTruncatedHeader, p.pos, p.path(), "unexpected EOF inside known-size element"}
				}
			}
			return nil, io.EOF
		}
		return nil, err
	}
	id := uint32(idRaw)
	lvl, known := elemLevel[id]
	generic := id == IDVoid || id == IDCRC32
	if !known && !generic {
		lvl = 0
		if len(p.stack) > 0 {
			if l, ok := elemLevel[p.stack[len(p.stack)-1].id]; ok {
				lvl = l + 1
			}
		}
	}
	// An unknown-size ancestor ends when an element of its own level or
	// shallower appears (legal parent boundary).
	for len(p.stack) > 0 {
		t := p.stack[len(p.stack)-1]
		tl, tk := elemLevel[t.id]
		if t.end == UnknownSize && known && !generic && tk && lvl <= tl {
			p.stack = p.stack[:len(p.stack)-1]
			continue
		}
		break
	}
	// The new element must nest exactly one level below the current parent.
	if known && !generic {
		ok := false
		if len(p.stack) == 0 {
			ok = lvl <= 1
		} else {
			tl, tk := elemLevel[p.stack[len(p.stack)-1].id]
			ok = !tk || lvl == tl+1
		}
		if !ok {
			return nil, &Error{ErrParentBoundary, idOff, p.path(),
				fmt.Sprintf("element %s (level %d) not allowed here", elemName(id), lvl)}
		}
	}

	sz, sw, szOff, err := p.readVINT(true)
	if err != nil {
		return nil, err
	}
	size := int64(sz)
	if sz == (uint64(1)<<(7*sw))-1 {
		size = UnknownSize
	}
	dataOff := p.pos
	master := masterIDs[id]
	if size == UnknownSize && !master {
		return nil, &Error{ErrUnknownSizeLeaf, szOff, p.path(),
			fmt.Sprintf("non-master element %s uses unknown size", elemName(id))}
	}
	end := UnknownSize
	if size != UnknownSize {
		if size > math.MaxInt64-dataOff {
			return nil, &Error{ErrSizeOverflow, szOff, p.path(), "element size overflows int64"}
		}
		end = dataOff + size
		for i := len(p.stack) - 1; i >= 0; i-- {
			if p.stack[i].end >= 0 {
				if end > p.stack[i].end {
					return nil, &Error{ErrSizeOverflow, szOff, p.path(),
						fmt.Sprintf("element %s size %d exceeds parent end %d", elemName(id), size, p.stack[i].end)}
				}
				break
			}
		}
		if p.total >= 0 && end > p.total {
			return nil, &Error{ErrSizeOverflow, szOff, p.path(),
				fmt.Sprintf("element %s size %d exceeds input length %d", elemName(id), size, p.total)}
		}
	}
	el := &Element{ID: id, Size: size, HeaderOffset: idOff, DataOffset: dataOff,
		Path: p.path() + "/" + elemName(id), Master: master}
	if master {
		p.stack = append(p.stack, openElem{id: id, end: end, path: el.Path})
	}
	return el, nil
}

// ReadData reads a leaf element's payload, enforcing a maximum size.
func (p *Parser) ReadData(el *Element, max int64) ([]byte, error) {
	if el.Size == UnknownSize {
		return nil, &Error{ErrUnknownSizeLeaf, el.HeaderOffset, el.Path, "cannot read unknown-size leaf"}
	}
	if el.Size > max {
		return nil, &Error{ErrBlockTooLarge, el.DataOffset, el.Path,
			fmt.Sprintf("element size %d exceeds limit %d", el.Size, max)}
	}
	if p.pos != el.DataOffset {
		return nil, &Error{ErrTruncatedHeader, p.pos, el.Path, "parser not positioned at element data"}
	}
	buf := make([]byte, el.Size)
	n, err := io.ReadFull(p.r, buf)
	p.pos += int64(n)
	if err != nil {
		return nil, &Error{ErrTruncatedHeader, p.pos, el.Path, "element data truncated"}
	}
	return buf, nil
}

// Skip discards a leaf element's payload.
func (p *Parser) Skip(el *Element) error {
	if el.Size == UnknownSize {
		return nil
	}
	n, err := io.CopyN(io.Discard, p.r, el.Size)
	p.pos += n
	if err != nil {
		return &Error{ErrTruncatedHeader, p.pos, el.Path, "element data truncated"}
	}
	return nil
}
