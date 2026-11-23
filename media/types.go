package media

import (
	"encoding/binary"
	"io"
)

// MaxInputSize bounds any single input file (default 20 GiB).
const MaxInputSize int64 = 20 << 30

// Box is a scanned top-level or nested box header with validated bounds.
type Box struct {
	Type    string `json:"type"`
	Offset  int64  `json:"offset"`
	Size    int64  `json:"size"`
	Header  int64  `json:"header"`
	Depth   int    `json:"depth"`
	Payload int64  `json:"payload_size"`
}

// EditEntry mirrors an edit list entry; segment duration is in movie ticks,
// media time in media ticks (-1 meaning an empty edit).
type EditEntry struct {
	SegmentDuration uint64 `json:"segment_duration"`
	MediaTime       int64  `json:"media_time"`
	Rate            int32  `json:"rate"`
}

// ScanBoxes walks one container's direct children with strict bounds checks.
// It never trusts the container size field: every box must fit the remainder.
func ScanBoxes(r io.ReadSeeker, size int64) ([]Box, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, WrapError(ClassIO, "scan.seek", 0, err, "rewind failed")
	}
	return scanBoxList(r, 0, size, 0)
}

// scanBoxList reads box headers in [start,end) and validates size accounting.
func scanBoxList(r io.ReadSeeker, start, end int64, depth int) ([]Box, error) {
	if end < start {
		return nil, NewError(ClassBoxBounds, "scan", start, "container end %d precedes start %d", end, start)
	}
	var out []Box
	pos := start
	hdr := make([]byte, 8)
	for pos < end {
		remaining := end - pos
		if remaining < 8 {
			return nil, NewError(ClassTruncated, "scan", pos, "only %d bytes remain for a box header", remaining)
		}
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return nil, WrapError(ClassIO, "scan.seek", pos, err, "seek failed")
		}
		if _, err := io.ReadFull(r, hdr); err != nil {
			return nil, WrapError(ClassTruncated, "scan.header", pos, err, "short box header")
		}
		sz := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		header := int64(8)
		switch sz {
		case 0:
			sz = remaining // extends to end of the enclosing container
		case 1:
			if remaining < 16 {
				return nil, NewError(ClassTruncated, "scan", pos, "64-bit box header truncated")
			}
			var large [8]byte
			if _, err := io.ReadFull(r, large[:]); err != nil {
				return nil, WrapError(ClassTruncated, "scan.header", pos, err, "short large box header")
			}
			u := binary.BigEndian.Uint64(large[:])
			if u > uint64(MaxInputSize) {
				return nil, NewError(ClassSizeOverflow, "scan", pos, "box %q size %d exceeds input limit", typ, u)
			}
			sz = int64(u)
			header = 16
		}
		if sz < header {
			return nil, NewError(ClassBoxBounds, "scan", pos, "box %q declared size %d smaller than header %d", typ, sz, header)
		}
		if sz > remaining {
			return nil, NewError(ClassBoxBounds, "scan", pos, "box %q size %d exceeds remaining %d", typ, sz, remaining)
		}
		out = append(out, Box{Type: typ, Offset: pos, Size: sz, Header: header, Depth: depth, Payload: sz - header})
		pos += sz
	}
	if pos != end {
		return nil, NewError(ClassBoxBounds, "scan", pos, "boxes do not consume container exactly (end=%d)", end)
	}
	return out, nil
}
