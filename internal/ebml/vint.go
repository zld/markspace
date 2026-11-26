package ebml

import (
	"encoding/binary"
	"fmt"
)

// EBML and Matroska element identifiers used by pwflow.
const (
	IDEBMLHeader    uint32 = 0x1A45DFA3
	IDSegment       uint32 = 0x18538067
	IDSeekHead      uint32 = 0x114D9B74
	IDInfo          uint32 = 0x1549A966
	IDTimecodeScale uint32 = 0x2AD7B1
	IDTracks        uint32 = 0x1654AE6B
	IDTrackEntry    uint32 = 0xAE
	IDTrackNumber   uint32 = 0xD7
	IDTrackType     uint32 = 0x83
	IDCodecID       uint32 = 0x86
	IDVideo         uint32 = 0xE0
	IDPixelWidth    uint32 = 0xB0
	IDPixelHeight   uint32 = 0xBA
	IDCluster       uint32 = 0x1F43B675
	IDTimecode      uint32 = 0xE7
	IDSimpleBlock   uint32 = 0xA3
	IDBlockGroup    uint32 = 0xA0
	IDBlock         uint32 = 0xA1
	IDCues          uint32 = 0x1C53BB6B
	IDTags          uint32 = 0x1254C367
	IDChapters      uint32 = 0x1043A770
	IDAttachments   uint32 = 0x1941A469
)

const unknownVINT = ^uint64(0)

// Header is a decoded EBML element header.
type Header struct {
	ID         uint32
	IDWidth    int
	Size       uint64
	SizeWidth  int
	HeaderSize int
	DataOffset int64
	Unknown    bool
}

// readVINT reads an EBML variable-length integer. When keepMarker is true
// the length marker bit is retained for element IDs; otherwise it is removed
// for element sizes.
func readVINT(data []byte, off int, keepMarker bool) (value uint64, width int, unknown bool, err error) {
	if off < 0 || off >= len(data) {
		return 0, 0, false, fmt.Errorf("vint offset %d outside %d bytes", off, len(data))
	}
	first := data[off]
	if first == 0 {
		return 0, 0, false, fmt.Errorf("non-canonical vint leading zero at %d", off)
	}
	width = 1
	for width <= 8 && first&(0x80>>uint(width-1)) == 0 {
		width++
	}
	if width > 8 || off+width > len(data) {
		return 0, width, false, fmt.Errorf("truncated vint at %d width %d", off, width)
	}
	var raw uint64
	for i := 0; i < width; i++ {
		raw = raw<<8 | uint64(data[off+i])
	}
	marker := uint64(1) << uint(8*width-width)
	if keepMarker {
		return raw, width, false, nil
	}
	value = raw & (marker - 1)
	// All data bits one means an unknown-sized element.
	dataBits := 7*width + (width - 1) // total bits minus width marker/leading-zero bits
	_ = dataBits
	lowMask := marker - 1
	unknown = value == lowMask
	return value, width, unknown, nil
}

// ReadHeader decodes an element ID and size at off.
func ReadHeader(data []byte, off int64) (Header, error) {
	id, idw, _, err := readVINT(data, int(off), true)
	if err != nil {
		return Header{}, err
	}
	sizeOff := int(off) + idw
	size, sw, unknown, err := readVINT(data, sizeOff, false)
	if err != nil {
		return Header{}, err
	}
	h := Header{
		ID:         uint32(id),
		IDWidth:    idw,
		Size:       size,
		SizeWidth:  sw,
		HeaderSize: idw + sw,
		DataOffset: off + int64(idw+sw),
		Unknown:    unknown,
	}
	return h, nil
}

// EncodeID encodes an element ID. It is primarily used by tests/fixtures.
func EncodeID(id uint32) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		b := []byte{0, 0}
		binary.BigEndian.PutUint16(b, uint16(id))
		return b
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, id)
		return b
	}
}

// EncodeSize encodes a known size in the shortest canonical VINT.
func EncodeSize(size uint64) []byte {
	switch {
	case size < (1<<7 - 1):
		return []byte{0x80 | byte(size)}
	case size < (1<<14 - 1):
		return []byte{0x40 | byte(size>>8), byte(size)}
	case size < (1<<21 - 1):
		return []byte{0x20 | byte(size>>16), byte(size >> 8), byte(size)}
	case size < (1<<28 - 1):
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(size))
		b[0] |= 0x10
		return b
	default:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], size)
		b[0] |= 0x01
		return b[:]
	}
}

// EncodeUnknownSize returns a width-byte unknown-size VINT.
func EncodeUnknownSize(width int) []byte {
	switch width {
	case 1:
		return []byte{0xFF}
	case 2:
		return []byte{0x7F, 0xFF}
	case 3:
		return []byte{0x3F, 0xFF, 0xFF}
	case 4:
		return []byte{0x1F, 0xFF, 0xFF, 0xFF}
	default:
		return []byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	}
}

func readUint(data []byte) uint64 {
	var v uint64
	for _, b := range data {
		v = v<<8 | uint64(b)
	}
	return v
}

func readString(data []byte) string { return string(data) }
