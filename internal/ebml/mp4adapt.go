package ebml

import (
	"github.com/abema/go-mp4"
)

// AsBoxInfo bridges an EBML element to go-mp4's BoxInfo offset/size model.
// The library's ExtendToEOF flag maps directly onto an EBML unknown-length
// element, which is why go-mp4 is used as the structural adaptation layer
// even though Matroska uses VINT headers rather than MP4 box headers.
func AsBoxInfo(el *Element) *mp4.BoxInfo {
	bi := &mp4.BoxInfo{
		Offset:     uint64(el.Offset),
		HeaderSize: uint64(el.HeaderLen),
		ExtendToEOF: el.Size < 0,
	}
	if el.Size >= 0 {
		bi.Size = uint64(el.HeaderLen) + uint64(el.Size)
	}
	return bi
}

// PayloadRange returns the [start, end) byte offsets of el's payload,
// resolving unknown length against the supplied enclosing boundary.
func PayloadRange(el *Element, boundary int64) (int64, int64) {
	if el.Size < 0 {
		return el.DataOffset, boundary
	}
	return el.DataOffset, el.EndOffset()
}
