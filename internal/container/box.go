// Package container parses and writes ISO BMFF / QuickTime MP4 and MOV box
// structures with strictly bounded reads. All offsets are validated against
// the actual file size before any seek.
package container

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"archivist/internal/model"
)

// FourCC is a four-character box type.
type FourCC [4]byte

func (t FourCC) String() string { return string(t[:]) }

// Well-known box types used by the archivist kernel.
var (
	BoxFtyp = FourCC{'f', 't', 'y', 'p'}
	BoxMoov = FourCC{'m', 'o', 'o', 'v'}
	BoxMvhd = FourCC{'m', 'v', 'h', 'd'}
	BoxTrak = FourCC{'t', 'r', 'a', 'k'}
	BoxTkhd = FourCC{'t', 'k', 'h', 'd'}
	BoxMdia = FourCC{'m', 'd', 'i', 'a'}
	BoxMdhd = FourCC{'m', 'd', 'h', 'd'}
	BoxHdlr = FourCC{'h', 'd', 'l', 'r'}
	BoxMinf = FourCC{'m', 'i', 'n', 'f'}
	BoxStbl = FourCC{'s', 't', 'b', 'l'}
	BoxStsd = FourCC{'s', 't', 's', 'd'}
	BoxStts = FourCC{'s', 't', 't', 's'}
	BoxStss = FourCC{'s', 't', 's', 's'}
	BoxStsc = FourCC{'s', 't', 's', 'c'}
	BoxStsz = FourCC{'s', 't', 's', 'z'}
	BoxStco = FourCC{'s', 't', 'c', 'o'}
	BoxCo64 = FourCC{'c', 'o', '6', '4'}
	BoxElst = FourCC{'e', 'l', 's', 't'}
	BoxEdts = FourCC{'e', 'd', 't', 's'}
	BoxColr = FourCC{'c', 'o', 'l', 'r'}
	BoxMdat = FourCC{'m', 'd', 'a', 't'}
	BoxFree = FourCC{'f', 'r', 'e', 'e'}
	BoxUdta = FourCC{'u', 'd', 't', 'a'}
)

// Container boxes whose payload is itself a sequence of boxes.
var containerTypes = map[FourCC]bool{
	BoxMoov: true, BoxTrak: true, BoxMdia: true, BoxMinf: true,
	BoxStbl: true, BoxEdts: true, BoxUdta: true,
}

// Box is one decoded box header plus raw payload bytes (kept bounded by the
// 512 MiB file limit) and decoded children for container boxes.
type Box struct {
	Type     FourCC
	Offset   uint64 // absolute offset of the header in the file
	Header   uint8  // 8 or 16
	Size32   uint32 // declared 32-bit size (1 means largesize)
	Payload  []byte // payload bytes for leaf boxes
	Children []*Box
	realSize uint64 // used when Size32 == 1 or size == 0
}

// Size returns the full on-disk box size including the header.
func (b *Box) Size() uint64 {
	if b.Size32 == 1 {
		// largesize lives in the payload prefix; caller fills RealSize.
		return b.realSize
	}
	return uint64(b.Size32)
}

// PayloadSize returns the declared payload length (size minus header).
func (b *Box) PayloadSize() uint64 {
	if b.Size() < uint64(b.Header) {
		return 0
	}
	return b.Size() - uint64(b.Header)
}

// ParseResult is the decoded top-level box tree plus the source length.
type ParseResult struct {
	Boxes      []*Box
	FileLength uint64
}

// boundedReader enforces every read against the declared file length.
type boundedReader struct {
	r    io.Reader
	pos  uint64
	size uint64
}

func (br *boundedReader) read(p []byte) error {
	if br.pos+uint64(len(p)) > br.size {
		return model.ErrTruncatedBox
	}
	n, err := io.ReadFull(br.r, p)
	br.pos += uint64(n)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return model.ErrTruncatedBox
		}
		return err
	}
	return nil
}

// Parse decodes the full box tree from a bounded byte source. data must be
// the complete file content; callers enforce the 512 MiB limit before use.
func Parse(data []byte) (*ParseResult, error) {
	if uint64(len(data)) > model.MaxFileBytes {
		return nil, model.AtTrack(model.StageContainer, model.ErrLimitExceeded, 0,
			fmt.Sprintf("file %d bytes exceeds %d", len(data), model.MaxFileBytes))
	}
	br := &boundedReader{r: bytes.NewReader(data), size: uint64(len(data))}
	boxes, err := parseBoxList(br, 0)
	if err != nil {
		return nil, err
	}
	return &ParseResult{Boxes: boxes, FileLength: uint64(len(data))}, nil
}

// ParseFrom is the streaming variant: it reads exactly length bytes through a
// buffered reader and then parses. It guards against declared sizes that run
// past the supplied length.
func ParseFrom(r io.Reader, length uint64) (*ParseResult, error) {
	if length > model.MaxFileBytes {
		return nil, model.AtTrack(model.StageContainer, model.ErrLimitExceeded, 0,
			fmt.Sprintf("declared %d bytes exceeds %d", length, model.MaxFileBytes))
	}
	buf := bufio.NewReaderSize(io.LimitReader(r, int64(length)), 1<<20)
	data, err := io.ReadAll(buf)
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) != length {
		return nil, model.ErrTruncatedBox
	}
	return Parse(data)
}

func parseBoxList(br *boundedReader, depth int) ([]*Box, error) {
	if depth > model.MaxBoxNesting {
		return nil, model.AtTrack(model.StageContainer, model.ErrUnsupportedBox, 0,
			fmt.Sprintf("nesting deeper than %d", model.MaxBoxNesting))
	}
	var out []*Box
	for br.pos < br.size {
		b, err := parseOneBox(br, depth)
		if err != nil {
			return out, err
		}
		out = append(out, b)
	}
	return out, nil
}

func parseOneBox(br *boundedReader, depth int) (*Box, error) {
	start := br.pos
	hdr := make([]byte, 8)
	if err := br.read(hdr); err != nil {
		return nil, err
	}
	size32 := binary.BigEndian.Uint32(hdr[0:4])
	cc := FourCC{hdr[4], hdr[5], hdr[6], hdr[7]}
	b := &Box{Type: cc, Offset: start, Size32: size32, Header: 8}
	headerLen := uint64(8)
	if size32 == 1 {
		large := make([]byte, 8)
		if err := br.read(large); err != nil {
			return nil, err
		}
		ls := binary.BigEndian.Uint64(large)
		if ls < 16 {
			return nil, model.AtTrack(model.StageContainer, model.ErrUnsupportedBox, 0,
				"largesize smaller than header")
		}
		b.Header = 16
		headerLen = 16
		b.realSize = ls
	} else if size32 < 8 && size32 != 0 {
		return nil, model.AtTrack(model.StageContainer, model.ErrUnsupportedBox, 0,
			fmt.Sprintf("box %s size %d < 8", cc, size32))
	}
	// size==0 means "box extends to end of file" — only valid at top level.
	var end uint64
	if size32 == 0 {
		end = br.size
		b.realSize = br.size - start
	} else {
		end = start + b.Size()
	}
	if end > br.size || end < start {
		return nil, model.AtTrack(model.StageContainer, model.ErrTruncatedBox, 0,
			fmt.Sprintf("box %s at %d declares end %d beyond file %d", cc, start, end, br.size))
	}
	payloadLen := end - start - headerLen
	payload := make([]byte, payloadLen)
	if err := br.read(payload); err != nil {
		return nil, err
	}
	if containerTypes[cc] {
		pbr := &boundedReader{r: bytes.NewReader(payload), size: payloadLen}
		children, err := parseBoxList(pbr, depth+1)
		if err != nil {
			return nil, err
		}
		b.Children = children
	} else {
		b.Payload = payload
	}
	return b, nil
}

// FindFirst returns the first box of the requested type at this level.
func FindFirst(boxes []*Box, t FourCC) *Box {
	for _, b := range boxes {
		if b.Type == t {
			return b
		}
	}
	return nil
}

// FindAll returns every box of the requested type at this level.
func FindAll(boxes []*Box, t FourCC) []*Box {
	var out []*Box
	for _, b := range boxes {
		if b.Type == t {
			out = append(out, b)
		}
	}
	return out
}
