package ebml

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"assetcheck/internal/media"
)

// EBML/Matroska IDs used by the local fixture subset. They are ordinary
// big-endian element IDs; only size fields use length-descriptor VINTs.
const (
	IDEBMLHeader  uint32 = 0x1A45DFA3
	IDVersion     uint32 = 0x4286
	IDReadVersion uint32 = 0x42F7
	IDDocType     uint32 = 0x4282

	IDSegment       uint32 = 0x18538067
	IDInfo          uint32 = 0x1549A966
	IDTimecodeScale uint32 = 0x2AD7B1
	IDMuxingApp     uint32 = 0x4D80
	IDWritingApp    uint32 = 0x5741

	IDTracks       uint32 = 0x1654AE6B
	IDTrackEntry   uint32 = 0xAE
	IDTrackNumber  uint32 = 0xD7
	IDTrackType    uint32 = 0x83
	IDLanguage     uint32 = 0x22B59C
	IDCodecID      uint32 = 0x86
	IDChannels     uint32 = 0x9F
	IDSamplingFreq uint32 = 0xB5
	IDBitDepth     uint32 = 0x6264

	IDCluster       uint32 = 0x1F43B675
	IDTimecode      uint32 = 0xE7
	IDSimpleBlock   uint32 = 0xA3
	IDBlockGroup    uint32 = 0xA0
	IDBlock         uint32 = 0xA1
	IDBlockDuration uint32 = 0x9B
)

const MaxVINTWidth = 8

// VINT is a decoded EBML length descriptor or raw element ID.
type VINT struct {
	Raw     uint64
	Value   uint64
	Width   int
	Unknown bool
	Offset  int64
}

// CountingReader reads a stream while retaining the absolute byte offset.
type CountingReader struct {
	r      io.Reader
	offset int64
}

func NewCountingReader(r io.Reader, startOffset int64) *CountingReader {
	return &CountingReader{r: r, offset: startOffset}
}

func (c *CountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.offset += int64(n)
	return n, err
}

func (c *CountingReader) ReadFull(p []byte) error {
	n, err := io.ReadFull(c, p)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return media.Corrupt("EBML_TRUNCATED_READ", "element header or field is truncated").
				WithElement("", c.offset-int64(len(p)-n)).Wrap(err)
		}
		return err
	}
	return nil
}

func (c *CountingReader) Offset() int64 { return c.offset }

// ReadVINT reads one length-descriptor VINT. A clean EOF before the first byte
// is returned as io.EOF so container loops can end normally.
func ReadVINT(c *CountingReader) (VINT, error) {
	start := c.Offset()
	var first [1]byte
	n, err := c.r.Read(first[:])
	c.offset += int64(n)
	if err != nil {
		if errors.Is(err, io.EOF) && n == 0 {
			return VINT{}, io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return VINT{}, media.Corrupt("EBML_TRUNCATED_VINT", "truncated VINT first byte").
				WithElement("", start).Wrap(err)
		}
		return VINT{}, err
	}
	if first[0] == 0x00 {
		return VINT{}, media.Corrupt("EBML_ILLEGAL_VINT", "VINT has no length marker bit").
			WithElement("", start)
	}

	width := 1
	for mask := byte(0x80); width <= MaxVINTWidth && first[0]&mask == 0; width++ {
		mask >>= 1
	}
	if width > MaxVINTWidth {
		return VINT{}, media.Corrupt("EBML_ILLEGAL_VINT", "VINT width exceeds 8 bytes").
			WithElement("", start)
	}

	buf := make([]byte, width)
	buf[0] = first[0]
	if width > 1 {
		if err := c.ReadFull(buf[1:]); err != nil {
			if me, ok := AsMediaError(err); ok && me.Offset < 0 {
				me.Offset = start
			}
			return VINT{}, err
		}
	}
	padded := make([]byte, 8)
	copy(padded[8-width:], buf)
	raw := binary.BigEndian.Uint64(padded)
	dataBits := 7 * width
	valueMask := uint64((1 << dataBits) - 1)
	value := raw & valueMask
	return VINT{
		Raw:     raw,
		Value:   value,
		Width:   width,
		Unknown: value == valueMask,
		Offset:  start,
	}, nil
}

func AsMediaError(err error) (*media.Error, bool) { return media.AsError(err) }

// ReadElementID reads the raw ID VINT without stripping its class marker.
func ReadElementID(c *CountingReader) (uint32, int, int64, error) {
	v, err := ReadVINT(c)
	if err != nil {
		return 0, 0, -1, err
	}
	if v.Unknown {
		return 0, v.Width, v.Offset, media.Corrupt("EBML_ILLEGAL_ID", "element ID VINT cannot be unknown-length").
			WithElement("", v.Offset)
	}
	if v.Raw > 0xFFFFFFFF {
		return 0, v.Width, v.Offset, media.Corrupt("EBML_ILLEGAL_ID", "element ID is wider than uint32").
			WithElement("", v.Offset)
	}
	return uint32(v.Raw), v.Width, v.Offset, nil
}

// EncodeSize encodes a known element payload size using its minimal VINT.
func EncodeSize(size uint64) ([]byte, error) {
	for width := 1; width <= MaxVINTWidth; width++ {
		dataBits := 7 * width
		unknownMask := uint64((1 << dataBits) - 1)
		if size <= unknownMask-1 {
			marker := uint64(1) << dataBits
			raw := size | marker
			out := make([]byte, width)
			for i := width - 1; i >= 0; i-- {
				out[i] = byte(raw & 0xff)
				raw >>= 8
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("size %d does not fit EBML VINT", size)
}

// EncodeUnknownSize returns the reserved all-ones size for the given width.
func EncodeUnknownSize(width int) ([]byte, error) {
	if width < 1 || width > MaxVINTWidth {
		return nil, fmt.Errorf("invalid unknown-size VINT width %d", width)
	}
	dataBits := 7 * width
	raw := (uint64(1) << dataBits) - 1
	raw |= uint64(1) << dataBits
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(raw & 0xff)
		raw >>= 8
	}
	return out, nil
}

func EncodeID(id uint32) []byte {
	if id <= 0xff {
		return []byte{byte(id)}
	}
	if id <= 0xffff {
		return []byte{byte(id >> 8), byte(id)}
	}
	if id <= 0xffffff {
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	}
	return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
}
