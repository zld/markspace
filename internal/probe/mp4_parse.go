package probe

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// sha256File hashes an already-open file without loading it into memory.
func sha256File(f *os.File, size int64) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	buf := make([]byte, 64*1024)
	var remaining int64 = size
	for remaining > 0 {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			remaining -= int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func u16at(r io.ReaderAt, off int64) uint16 {
	if b := readN(r, off, 2); b != nil {
		return binary.BigEndian.Uint16(b)
	}
	return 0
}

func u32at(r io.ReaderAt, off int64) uint32 {
	if b := readN(r, off, 4); b != nil {
		return binary.BigEndian.Uint32(b)
	}
	return 0
}

// trackIDAt reads tkhd track_ID (offset differs by box version).
func trackIDAt(r io.ReaderAt, off, len int64) uint32 {
	if len < 5 {
		return 0
	}
	ver := readN(r, off, 1)
	if ver == nil {
		return 0
	}
	if ver[0] == 1 {
		return u32at(r, off+4+8+8) // vf + creation + modification
	}
	return u32at(r, off+4+4+4)
}

// rotationAt classifies the tkhd 16.16 transformation matrix into an exact
// quarter-turn. Non-quarter values are retained raw but normalize to 0.
func rotationAt(r io.ReaderAt, off, len, boxOff int64) *SourcedValue {
	if len < 5 {
		return nil
	}
	ver := readN(r, off, 1)
	if ver == nil {
		return nil
	}
	matrixOff := off + 40
	if ver[0] == 1 {
		matrixOff = off + 52
	}
	m := readN(r, matrixOff, 36)
	if m == nil {
		return nil
	}
	a := int32(binary.BigEndian.Uint32(m[0:4]))
	b := int32(binary.BigEndian.Uint32(m[4:8]))
	c := int32(binary.BigEndian.Uint32(m[12:16]))
	d := int32(binary.BigEndian.Uint32(m[16:20]))
	one := int32(1 << 16)
	deg := int64(0)
	switch {
	case a == one && d == one && b == 0 && c == 0:
		deg = 0
	case a == 0 && d == 0 && b == one && c == -one:
		deg = 90
	case a == -one && d == -one && b == 0 && c == 0:
		deg = 180
	case a == 0 && d == 0 && b == -one && c == one:
		deg = 270
	default:
		deg = 0
	}
	return &SourcedValue{
		Source: "tkhd/matrix", Offset: boxOff,
		Raw:   fmt.Sprintf("a=%d b=%d c=%d d=%d", a, b, c, d),
		Value: deg,
	}
}

// handlerTypeAt reads hdlr handler_type at payload offset 8.
func handlerTypeAt(r io.ReaderAt, off, len int64) string {
	if len < 12 {
		return ""
	}
	b := readN(r, off+8, 4)
	if b == nil {
		return ""
	}
	return string(b)
}

// mdhdAt reads timescale/duration; payload layout depends on version.
func mdhdAt(r io.ReaderAt, off, len int64) (timescale uint32, firstTS, duration int64) {
	if len < 5 {
		return 0, 0, 0
	}
	ver := readN(r, off, 1)
	if ver == nil {
		return 0, 0, 0
	}
	if ver[0] == 1 {
		if len < 32 {
			return 0, 0, 0
		}
		timescale = u32at(r, off+20)
		duration = int64(binary.BigEndian.Uint64(readN(r, off+24, 8)))
		return timescale, 0, duration
	}
	if len < 20 {
		return 0, 0, 0
	}
	timescale = u32at(r, off+12)
	duration = int64(u32at(r, off+16))
	return timescale, 0, duration
}

// colrAt records every raw color declaration with its byte interval.
func colrAt(r io.ReaderAt, off, len, boxOff int64) ColorInfo {
	ci := ColorInfo{}
	ct := readN(r, off, 4)
	if ct == nil {
		return ci
	}
	colorType := string(ct)
	if colorType == "nclx" && len >= 10 {
		ci.Primaries = []SourcedValue{{
			Source: "colr/nclx/primaries", Offset: boxOff,
			Raw: fmt.Sprintf("%d", u16at(r, off+4)), Value: int64(u16at(r, off+4)),
		}}
		ci.Transfer = []SourcedValue{{
			Source: "colr/nclx/transfer", Offset: boxOff,
			Raw: fmt.Sprintf("%d", u16at(r, off+6)), Value: int64(u16at(r, off+6)),
		}}
		ci.Matrix = []SourcedValue{{
			Source: "colr/nclx/matrix", Offset: boxOff,
			Raw: fmt.Sprintf("%d", u16at(r, off+8)), Value: int64(u16at(r, off+8)),
		}}
		return ci
	}
	// ICC profile variants: retain the declaration, not the blob itself.
	if colorType == "prof" || colorType == "rICC" {
		ci.ICCProfile = []SourcedValue{{
			Source: "colr/" + colorType, Offset: boxOff,
			Raw: fmt.Sprintf("icc-profile-bytes=%d", len-4), Value: colorType,
		}}
	}
	return ci
}

// stszCountAt reads sample_count from stsz at payload offset 8.
func stszCountAt(r io.ReaderAt, off, len int64) int {
	if len < 12 {
		return -1
	}
	return int(u32at(r, off+8))
}
