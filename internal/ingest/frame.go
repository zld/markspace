// Package ingest parses project-defined frame records and cross-node RTP
// metadata. Frame parsing is streaming and bounds memory independently of the
// caller-supplied length field.
package ingest

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"medpipe/internal/clock"
)

const (
	// FrameMagic identifies version 1 frame records ("MDF1").
	FrameMagic = "MDF1"
	// FrameHeaderSize is the fixed header including the SHA-256 field.
	FrameHeaderSize = 72
	// SHA256Size is the checksum width in bytes.
	SHA256Size = sha256.Size
	// PixelRGB24 identifies three bytes per pixel in RGB order.
	PixelRGB24 uint8 = 1
	// DefaultMaxFrameBytes bounds buffered pixel payloads.
	DefaultMaxFrameBytes = 4 << 20
	// MaxDimension bounds width and height before multiplication.
	MaxDimension = 8192
)

// ColorMetadata describes the encoded color space.
type ColorMetadata struct {
	Primaries uint8 `json:"primaries"`
	Transfer  uint8 `json:"transfer"`
	Matrix    uint8 `json:"matrix"`
	FullRange bool  `json:"full_range"`
}

// Frame is one validated image-frame record.
type Frame struct {
	TrackID     uint16
	PTS         int64
	Timebase    clock.Timebase
	Width       int
	Height      int
	KeyFrame    bool
	PixelFormat uint8
	Color       ColorMetadata
	Pixels      []byte
	SHA256      [SHA256Size]byte
	Offset      int64
}

func pixelLength(width, height int) (int64, error) {
	if width <= 0 || height <= 0 || width > MaxDimension || height > MaxDimension {
		return 0, fmt.Errorf("invalid dimensions %dx%d", width, height)
	}
	w := uint64(width)
	h := uint64(height)
	if w > uint64(DefaultMaxFrameBytes)/(3*h) {
		return 0, fmt.Errorf("frame %dx%d exceeds byte limit", width, height)
	}
	return int64(w * h * 3), nil
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// EncodeFrame writes one stable big-endian record and fills its checksum.
func EncodeFrame(f *Frame) ([]byte, error) {
	if f == nil {
		return nil, errors.New("nil frame")
	}
	if err := f.Timebase.Validate(); err != nil {
		return nil, err
	}
	if f.Width <= 0 || f.Height <= 0 || f.Width > MaxDimension || f.Height > MaxDimension {
		return nil, fmt.Errorf("invalid dimensions %dx%d", f.Width, f.Height)
	}
	if f.PixelFormat != PixelRGB24 {
		return nil, fmt.Errorf("unsupported pixel format %d", f.PixelFormat)
	}
	want, err := pixelLength(f.Width, f.Height)
	if err != nil {
		return nil, err
	}
	if int64(len(f.Pixels)) != want {
		return nil, fmt.Errorf("pixel length %d does not match %dx%d RGB24 length %d", len(f.Pixels), f.Width, f.Height, want)
	}
	sum := sha256.Sum256(f.Pixels)
	f.SHA256 = sum

	buf := make([]byte, FrameHeaderSize+len(f.Pixels))
	copy(buf[0:4], FrameMagic)
	buf[4] = 1
	var flags byte
	if f.KeyFrame {
		flags |= 1
	}
	buf[5] = flags
	binary.BigEndian.PutUint16(buf[6:8], f.TrackID)
	binary.BigEndian.PutUint64(buf[8:16], uint64(f.PTS))
	binary.BigEndian.PutUint32(buf[16:20], uint32(f.Timebase.Numerator))
	binary.BigEndian.PutUint32(buf[20:24], uint32(f.Timebase.Denominator))
	binary.BigEndian.PutUint16(buf[24:26], uint16(f.Width))
	binary.BigEndian.PutUint16(buf[26:28], uint16(f.Height))
	buf[28] = f.PixelFormat
	buf[29] = f.Color.Primaries
	buf[30] = f.Color.Transfer
	buf[31] = f.Color.Matrix
	buf[32] = boolByte(f.Color.FullRange)
	binary.BigEndian.PutUint32(buf[36:40], uint32(len(f.Pixels)))
	copy(buf[40:72], f.SHA256[:])
	copy(buf[72:], f.Pixels)
	return buf, nil
}
