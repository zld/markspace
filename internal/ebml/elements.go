package ebml

import (
	"encoding/binary"
	"math"
)

// Matroska/WebM element IDs used by the parser, fixture generator and remuxer.
// Only the subset needed for multi-track block transport is modeled; unknown
// IDs are still parsed and skipped transparently by Reader.
const (
	IDEBMLExtra       uint32 = 0x1A45DFA3 // EBML header
	IDSegment         uint32 = 0x18538067
	IDSeekHead        uint32 = 0x114D9B74
	IDInfo            uint32 = 0x1549A966
	IDTimecodeScale   uint32 = 0x2AD7B1
	IDDuration        uint32 = 0x4489
	IDMuxingApp       uint32 = 0x4D80
	IDWritingApp      uint32 = 0x5741
	IDTracks          uint32 = 0x1654AE6B
	IDTrackEntry      uint32 = 0xAE
	IDTrackNumber     uint32 = 0xD7
	IDTrackUID        uint32 = 0x73C5
	IDTrackType       uint32 = 0x83
	IDCodecID         uint32 = 0x86
	IDCodecPrivate    uint32 = 0x63A2
	IDDefaultDuration uint32 = 0x23E383
	IDFlagLacing      uint32 = 0x9C
	IDAudio           uint32 = 0xE1
	IDSamplingFreq    uint32 = 0xB5
	IDChannels        uint32 = 0x9F
	IDVideo           uint32 = 0xE0
	IDPixelWidth      uint32 = 0xB0
	IDPixelHeight     uint32 = 0xBA
	IDCluster         uint32 = 0x1F43B675
	IDTimecode        uint32 = 0xE7
	IDSimpleBlock     uint32 = 0xA3
	IDBlockGroup      uint32 = 0xA0
	IDBlock           uint32 = 0xA1
)

// isContainerID reports whether an ID is a master element. Anything not in
// this table is treated as a scalar and read/skipped via ReadData.
func isContainerID(id uint32) bool {
	switch id {
	case IDEBMLExtra, IDSegment, IDSeekHead, IDInfo, IDTracks, IDTrackEntry,
		IDAudio, IDVideo, IDCluster, IDBlockGroup:
		return true
	}
	return false
}

// EncodeVINT writes a variable-size integer. When withLength is true the
// VINT_WIDTH marker is retained (element IDs); otherwise data is emitted in
// the smallest width that fits and the marker bit is set.
func EncodeVINT(v uint64, withLength bool) []byte {
	if withLength {
		// IDs are passed already carrying their own VINT marker; choose the
		// width by locating the highest set byte. EncodeID is the normal path.
		width := 1
		for x := v; x > 0xFF; x >>= 8 {
			width++
		}
		out := make([]byte, width)
		for i := width - 1; i >= 0; i-- {
			out[i] = byte(v >> uint(8*(width-1-i)))
		}
		return out
	}
	// Data VINT: width n holds n*7 bits; the all-ones pattern is reserved for
	// the unknown-length marker, so the max representable value is 2^(7n)-2.
	width := 1
	for v >= (uint64(1)<<uint(7*width))-1 {
		width++
	}
	val := v | (uint64(1) << uint(7*width)) // set marker bit at position 7n
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(val >> uint(8*(width-1-i)))
	}
	return out
}

// EncodeID emits an element ID (VINT with its length marker preserved).
func EncodeID(id uint32) []byte {
	width := 1
	switch {
	case id >= 1<<24:
		width = 4
	case id >= 1<<16:
		width = 3
	case id >= 1<<8:
		width = 2
	}
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(id >> uint(8*(width-1-i)))
	}
	return out
}

// EncodeUnknownSize emits the canonical 1-byte unknown-length marker
// (VINT_WIDTH 1, all data bits set: 0xFF).
func EncodeUnknownSize() []byte { return []byte{0xFF} }

// EncodeElement emits a scalar element (ID, size VINT, payload).
func EncodeElement(id uint32, payload []byte) []byte {
	out := append(EncodeID(id), EncodeVINT(uint64(len(payload)), false)...)
	return append(out, payload...)
}

// EncodeUint encodes an EBML unsigned integer payload.
func EncodeUint(v uint64) []byte {
	if v == 0 {
		return []byte{0}
	}
	var tmp [8]byte
	n := 0
	for x := v; x > 0; x >>= 8 {
		tmp[7-n] = byte(x)
		n++
	}
	return tmp[8-n:]
}

// EncodeFloat64 encodes an 8-byte EBML float payload.
func EncodeFloat64(f float64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, math.Float64bits(f))
	return out
}

// MasterBuilder accumulates child bytes so a master element can be emitted
// with its final, known size. Output is therefore deterministic and does not
// rely on unknown-length encoding for the happy path.
type MasterBuilder struct{ buf []byte }

func (m *MasterBuilder) Write(p []byte) { m.buf = append(m.buf, p...) }
func (m *MasterBuilder) Uint(id uint32, v uint64) {
	m.Write(EncodeElement(id, EncodeUint(v)))
}
func (m *MasterBuilder) Float(id uint32, f float64) {
	m.Write(EncodeElement(id, EncodeFloat64(f)))
}
func (m *MasterBuilder) String(id uint32, s string) {
	m.Write(EncodeElement(id, []byte(s)))
}
func (m *MasterBuilder) Bytes(id uint32, b []byte) {
	m.Write(EncodeElement(id, b))
}
func (m *MasterBuilder) Master(id uint32, child *MasterBuilder) {
	m.Write(EncodeElement(id, child.buf))
}
func (m *MasterBuilder) BytesValue() []byte { return m.buf }

// Build wraps b in an element with id and returns the serialized bytes.
func Build(id uint32, b *MasterBuilder) []byte { return EncodeElement(id, b.buf) }
