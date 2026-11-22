// Package ebml implements streaming parsing of Matroska/WebM EBML
// elements, track and block boundary scanning, and digest-bound offset
// indexes. All reads go through io.ReaderAt so arbitrarily large files
// are handled without loading media payloads into memory.
package ebml

import (
	"encoding/binary"
	"io"

	"mediaqc/internal/timeline"
)

// Matroska/WebM element IDs used by the scanner.
const (
	IDEBML            uint32 = 0x1A45DFA3
	IDSegment         uint32 = 0x18538067
	IDInfo            uint32 = 0x1549A966
	IDTimestampScale  uint32 = 0x2AD7B1
	IDTracks          uint32 = 0x1654AE6B
	IDTrackEntry      uint32 = 0xAE
	IDTrackNumber     uint32 = 0xD7
	IDTrackType       uint32 = 0x83
	IDCodecID         uint32 = 0x86
	IDCluster         uint32 = 0x1F43B675
	IDTimestamp       uint32 = 0xE7
	IDSimpleBlock     uint32 = 0xA3
	IDBlockGroup      uint32 = 0xA0
	IDBlock           uint32 = 0xA1
	IDReferenceBlock  uint32 = 0xFB
	UnknownSize       int64  = -1
	maxElementPayload       = 1 << 62 // guard against absurd sizes
)

// Element is a parsed EBML element header.
type Element struct {
	ID       uint32
	Size     int64 // payload size in bytes, UnknownSize (-1) if unknown
	DataOff  int64 // absolute offset of the payload
	TotalLen int64 // header + payload length; -1 when Size is unknown
}

// End returns the absolute end offset of the element. For unknown-size
// elements it returns the supplied parent limit.
func (e Element) End(limit int64) int64 {
	if e.Size == UnknownSize {
		return limit
	}
	return e.DataOff + e.Size
}

// readVINT reads an EBML variable-length integer at off. When strip is
// true the length-marker bit is removed (size encoding), otherwise the
// raw value is kept (element ID encoding). Returns the value, the total
// byte length and a fault positioned at off on truncation.
func readVINT(r io.ReaderAt, off, limit int64, strip bool) (uint64, int64, error) {
	var b [1]byte
	if off+1 > limit {
		return 0, 0, timeline.NewFault(timeline.ClassTruncated, off, 0,
			"vint header beyond limit %d", limit)
	}
	if _, err := r.ReadAt(b[:], off); err != nil {
		return 0, 0, timeline.NewFault(timeline.ClassTruncated, off, 0, "%v", err)
	}
	first := b[0]
	if first == 0 {
		return 0, 0, timeline.NewFault(timeline.ClassSizeOverflow, off, 0,
			"invalid vint: zero length descriptor")
	}
	length := 1
	for mask := byte(0x80); first&mask == 0; mask >>= 1 {
		length++
	}
	if off+int64(length) > limit {
		return 0, 0, timeline.NewFault(timeline.ClassTruncated, off, 0,
			"vint of %d bytes crosses limit %d", length, limit)
	}
	buf := make([]byte, length)
	if _, err := r.ReadAt(buf, off); err != nil {
		return 0, 0, timeline.NewFault(timeline.ClassTruncated, off, 0, "%v", err)
	}
	val := uint64(buf[0])
	if strip {
		val &= uint64(0xFF >> uint(length))
	}
	for i := 1; i < length; i++ {
		val = val<<8 | uint64(buf[i])
	}
	return val, int64(length), nil
}

// ReadElement parses the element header starting at off. limit is the
// exclusive end of the enclosing element (or file size). Declared sizes
// that cross the limit or overflow are reported as positioned faults.
func ReadElement(r io.ReaderAt, off, limit int64) (Element, error) {
	id, idLen, err := readVINT(r, off, limit, false)
	if err != nil {
		return Element{}, err
	}
	sizeVal, sizeLen, err := readVINT(r, off+idLen, limit, true)
	if err != nil {
		return Element{}, err
	}
	el := Element{ID: uint32(id), DataOff: off + idLen + sizeLen}
	// Unknown size: every data bit set (all 0xFF payload bits).
	allOnes := uint64(1)<<(7*uint(sizeLen)) - 1
	if sizeVal == allOnes {
		el.Size = UnknownSize
		el.TotalLen = -1
		return el, nil
	}
	if sizeVal > maxElementPayload {
		return Element{}, timeline.NewFault(timeline.ClassSizeOverflow, off, 0,
			"element 0x%x declares absurd size %d", el.ID, sizeVal)
	}
	el.Size = int64(sizeVal)
	el.TotalLen = idLen + sizeLen + el.Size
	if el.DataOff+el.Size > limit {
		return Element{}, timeline.NewFault(timeline.ClassTruncated, off, 0,
			"element 0x%x of %d bytes crosses limit %d", el.ID, el.Size, limit)
	}
	return el, nil
}

// IsMaster reports whether id is a known master (container) element.
func IsMaster(id uint32) bool {
	switch id {
	case IDSegment, IDInfo, IDTracks, IDTrackEntry, IDCluster, IDBlockGroup:
		return true
	}
	return false
}

// ReadUint reads an unsigned integer element payload (1-8 bytes).
func ReadUint(r io.ReaderAt, el Element) (uint64, error) {
	if el.Size < 1 || el.Size > 8 {
		return 0, timeline.NewFault(timeline.ClassSizeOverflow, el.DataOff, 0,
			"uint element 0x%x has size %d", el.ID, el.Size)
	}
	buf := make([]byte, el.Size)
	if _, err := r.ReadAt(buf, el.DataOff); err != nil {
		return 0, timeline.NewFault(timeline.ClassTruncated, el.DataOff, 0, "%v", err)
	}
	var v uint64
	for _, b := range buf {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// ReadString reads a string element payload.
func ReadString(r io.ReaderAt, el Element) (string, error) {
	if el.Size < 0 || el.Size > 1<<20 {
		return "", timeline.NewFault(timeline.ClassSizeOverflow, el.DataOff, 0,
			"string element 0x%x has size %d", el.ID, el.Size)
	}
	buf := make([]byte, el.Size)
	if _, err := r.ReadAt(buf, el.DataOff); err != nil {
		return "", timeline.NewFault(timeline.ClassTruncated, el.DataOff, 0, "%v", err)
	}
	return string(buf), nil
}

// Block is a parsed SimpleBlock/Block header plus payload extents.
type Block struct {
	Track    uint64
	Timecode int16 // relative to the enclosing cluster timestamp
	Keyframe bool
	// PayloadOff and PayloadLen locate the frame data in the input.
	PayloadOff int64
	PayloadLen int64
}

// maxBlockPayload bounds a single block read (256 MiB).
const maxBlockPayload = 1 << 28

// ParseBlock parses a block payload (track number vint, int16 timecode,
// flags byte, then frame data). data is the element payload; dataOff is
// its absolute offset in the input, used for fault reporting.
func ParseBlock(data []byte, dataOff int64, simple bool) (Block, error) {
	if len(data) < 4 {
		return Block{}, timeline.NewFault(timeline.ClassTruncated, dataOff, 0,
			"block header of %d bytes too short", len(data))
	}
	first := data[0]
	if first == 0 {
		return Block{}, timeline.NewFault(timeline.ClassSizeOverflow, dataOff, 0,
			"invalid track number vint")
	}
	tlen := 1
	for mask := byte(0x80); first&mask == 0; mask >>= 1 {
		tlen++
	}
	if len(data) < tlen+3 {
		return Block{}, timeline.NewFault(timeline.ClassTruncated, dataOff, 0,
			"block header crosses element boundary")
	}
	track := uint64(data[0] & (0xFF >> uint(tlen)))
	for i := 1; i < tlen; i++ {
		track = track<<8 | uint64(data[i])
	}
	tc := int16(binary.BigEndian.Uint16(data[tlen : tlen+2]))
	flags := data[tlen+2]
	if flags&0x06 != 0 {
		return Block{}, timeline.NewFault(timeline.ClassInternal, dataOff, track,
			"laced blocks are not supported (flags 0x%02x)", flags)
	}
	b := Block{
		Track:      track,
		Timecode:   tc,
		PayloadOff: dataOff + int64(tlen) + 3,
		PayloadLen: int64(len(data)) - int64(tlen) - 3,
	}
	if simple {
		b.Keyframe = flags&0x80 != 0
	} else {
		b.Keyframe = true // BlockGroup without ReferenceBlock: treat as key
	}
	if b.PayloadLen > maxBlockPayload {
		return Block{}, timeline.NewFault(timeline.ClassSizeOverflow, dataOff, track,
			"block payload %d bytes exceeds limit", b.PayloadLen)
	}
	return b, nil
}
