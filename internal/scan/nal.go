// Package scan extracts complete access units from raw elementary streams.
// Truncated units are rejected: a stream must terminate cleanly.
package scan

import "mediarc/internal/model"

// Codec identifies the elementary-stream encoding.
type Codec int

const (
	CodecH264 Codec = iota
	CodecH265
	CodecAV1
)

func (c Codec) String() string {
	switch c {
	case CodecH264:
		return "h264"
	case CodecH265:
		return "h265"
	case CodecAV1:
		return "av1"
	}
	return "unknown"
}

// AU is one complete access unit located in a stream.
type AU struct {
	Offset   int64 // byte offset of the first byte of the AU
	Length   int64 // total byte length
	Keyframe bool  // AU contains an IDR / key frame
}

// nalTypes holds the NAL unit type numbers that delimit structure.
type nalTypes struct {
	aud int          // access-unit delimiter: starts a new AU
	eos int          // end-of-stream: required terminator
	idr map[int]bool // VCL types marking a keyframe
}

var h264Types = nalTypes{aud: 9, eos: 11, idr: map[int]bool{5: true}}
var h265Types = nalTypes{aud: 35, eos: 36, idr: map[int]bool{19: true, 20: true}}

type nal struct {
	start int // offset of first byte of the start code (incl. zero_byte)
	typ   int
}

func nalHeaderType(b byte, c Codec) int {
	if c == CodecH265 {
		return int(b>>1) & 0x3f
	}
	return int(b) & 0x1f
}

// findStartCodes locates annex-b start codes (00 00 01, optionally with a
// leading zero_byte) and returns the NAL units they introduce.
func findStartCodes(data []byte, c Codec) []nal {
	var out []nal
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			start := i
			if i > 0 && data[i-1] == 0 {
				start = i - 1 // absorb zero_byte of a 4-byte start code
			}
			out = append(out, nal{start: start, typ: nalHeaderType(data[i+3], c)})
			i += 3
		}
	}
	return out
}

// ScanNAL scans an H.264 or H.265 annex-b stream into access units. AUs are
// delimited by AUD NALs; the stream must end with an end-of-stream NAL,
// otherwise the final AU is rejected as truncated and the error reports the
// byte offset where data ran out.
func ScanNAL(data []byte, c Codec) ([]AU, *model.Error) {
	nt := h264Types
	if c == CodecH265 {
		nt = h265Types
	}
	nals := findStartCodes(data, c)
	if len(nals) == 0 {
		return nil, &model.Error{Code: model.ErrMisaligned, Offset: 0, Msg: "no annex-b start code found"}
	}
	if nals[0].start != 0 {
		return nil, &model.Error{Code: model.ErrMisaligned, Offset: int64(nals[0].start), Msg: "data before first start code"}
	}
	var aus []AU
	cur := -1
	eos := false
	for i, n := range nals {
		end := len(data)
		if i+1 < len(nals) {
			end = nals[i+1].start
		}
		switch {
		case n.typ == nt.eos:
			eos = true
		case n.typ == nt.aud:
			cur = len(aus)
			aus = append(aus, AU{Offset: int64(n.start), Length: int64(end - n.start)})
		default:
			if cur < 0 {
				return nil, &model.Error{Code: model.ErrMisaligned, Offset: int64(n.start), Msg: "VCL NAL before first delimiter"}
			}
			if nt.idr[n.typ] {
				aus[cur].Keyframe = true
			}
			aus[cur].Length = int64(end) - aus[cur].Offset
		}
	}
	if !eos {
		if len(aus) > 0 {
			aus = aus[:len(aus)-1] // final AU may be incomplete: reject it
		}
		return aus, &model.Error{Code: model.ErrTruncated, Offset: int64(len(data)), Msg: "stream ends without end-of-stream NAL"}
	}
	return aus, nil
}
