// Package bitstream scans raw H.264, H.265 and AV1 elementary streams. It
// locates access-unit boundaries and random access frames so that an external
// keyframe index can be checked against the bytes it claims to describe.
package bitstream

import (
	"strings"

	"mediacore/internal/fault"
)

// Codec names an elementary stream format. The values are part of the CLI and
// manifest contract.
type Codec string

const (
	CodecH264 Codec = "h264"
	CodecH265 Codec = "h265"
	CodecAV1  Codec = "av1"
)

// ParseCodec accepts the documented spellings. H.265 is also known as HEVC.
func ParseCodec(s string) (Codec, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "h264", "avc", "avc1":
		return CodecH264, nil
	case "h265", "hevc", "hvc1", "hev1":
		return CodecH265, nil
	case "av1", "av01":
		return CodecAV1, nil
	default:
		return "", fault.Newf(fault.CodeScanUnsupportedCodec, s, "unsupported codec %q", s)
	}
}

func (c Codec) String() string { return string(c) }

// Frame describes one coded picture in decode order.
type Frame struct {
	Index      int   `json:"index"`
	Offset     int64 `json:"offset"`
	Size       int64 `json:"size"`
	Keyframe   bool  `json:"keyframe"`
	TemporalID int   `json:"temporal_id"`
}

// Stream is the result of scanning an elementary stream.
type Stream struct {
	Codec       Codec   `json:"codec"`
	Size        int64   `json:"size"`
	Frames      []Frame `json:"frames"`
	ParameterOK bool    `json:"parameter_ok"`
}

// KeyframeOffsets returns the byte offset of every random access frame.
func (s *Stream) KeyframeOffsets() []int64 {
	out := make([]int64, 0, len(s.Frames))
	for _, f := range s.Frames {
		if f.Keyframe {
			out = append(out, f.Offset)
		}
	}
	return out
}

// FrameAtOffset returns the frame beginning at exactly offset.
func (s *Stream) FrameAtOffset(offset int64) (Frame, bool) {
	for _, f := range s.Frames {
		if f.Offset == offset {
			return f, true
		}
	}
	return Frame{}, false
}
