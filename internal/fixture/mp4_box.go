package fixture

import (
	"bytes"
	"encoding/binary"
)

// mp4Box appends a size-prefixed box to w. Size includes the 8-byte
// header, as required by ISO/IEC 14496-12.
func mp4Box(w *bytes.Buffer, typ string, payload []byte) {
	if len(typ) != 4 {
		panic("fixture: box type must be 4 bytes: " + typ)
	}
	_ = binary.Write(w, binary.BigEndian, uint32(8+len(payload)))
	w.WriteString(typ)
	w.Write(payload)
}

// mp4FullBox prefixes payload with the version/flags full-box header.
func mp4FullBox(version byte, flags uint32, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteByte(version)
	b.WriteByte(byte(flags >> 16))
	b.WriteByte(byte(flags >> 8))
	b.WriteByte(byte(flags))
	b.Write(payload)
	return b.Bytes()
}

func putU16(b []byte, v uint16) { binary.BigEndian.PutUint16(b, v) }
func putU32(b []byte, v uint32) { binary.BigEndian.PutUint32(b, v) }
func putU64(b []byte, v uint64) { binary.BigEndian.PutUint64(b, v) }

// mp4Fixed16 renders a 16.16 fixed-point value.
func mp4Fixed16(v float64) uint32 { return uint32(v*65536.0 + 0.5) }

// identityMatrix is the standard ISO track matrix (16.16 fixed point).
var identityMatrix = [9]uint32{
	0x00010000, 0, 0,
	0, 0x00010000, 0,
	0, 0, 0x40000000,
}

// rotationMatrix returns the track matrix for rotation in {0,90,180,270}
// degrees, expressed in the counter-clockwise ISO convention.
func rotationMatrix(degrees int) [9]uint32 {
	m := identityMatrix
	switch degrees {
	case 90:
		// 90 CCW: [0,1; -1,0]
		m = [9]uint32{0, 0x00010000, 0, 0xFFFF0000, 0, 0, 0, 0, 0x40000000}
	case 180:
		m = [9]uint32{0xFFFF0000, 0, 0, 0, 0xFFFF0000, 0, 0, 0, 0x40000000}
	case 270:
		m = [9]uint32{0, 0xFFFF0000, 0, 0x00010000, 0, 0, 0, 0, 0x40000000}
	}
	return m
}

const (
	mp4VideoHandler = "vide"
	mp4AudioHandler = "soun"
)

// SampleDesc describes one generated coded sample.
type SampleDesc struct {
	Track    uint32
	Index    uint32 // 1-based sample number within the track
	Offset   int64  // absolute file offset of the sample bytes
	Size     uint32
	TimeTick int64  // presentation time in master ticks
	Keyframe bool
	Complete bool // false when truncation cut this sample short
}

// MP4Manifest captures the generator ground truth for later assertions.
type MP4Manifest struct {
	Samples       []SampleDesc
	VideoTimescale uint32
	AudioTimescale uint32
	Width, Height  uint32
	Rotation       int
	ColorPrimaries uint16
}
