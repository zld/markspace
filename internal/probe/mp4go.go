package probe

import (
	"bytes"

	mp4 "github.com/abema/go-mp4"

	"tracecheck/internal/clock"
)

// boxHeader is the go-mp4 validated view of one box header. The custom
// scanner remains authoritative for bounds and original offsets; go-mp4 is
// used here as the spec-correct header parser and supported-type oracle.
type boxHeader struct {
	size       int64
	headerSize int64
	boxType    string
	supported  bool
}

// readBoxHeader parses the header at data[start:] through go-mp4. The region
// limit is supplied so size==0 (to-EOF) boxes resolve against the enclosing
// container rather than the whole slice.
func readBoxHeader(data []byte, start, limit int64) (boxHeader, *clock.OffsetError) {
	region := data[start:limit]
	r := bytes.NewReader(region)
	bi, err := mp4.ReadBoxInfo(r)
	if err != nil {
		return boxHeader{}, clock.NewOffsetError(clock.Offset(start),
			clock.CategoryTruncated, "go-mp4 rejected box header", err)
	}
	return boxHeader{
		size:       int64(bi.Size),
		headerSize: int64(bi.HeaderSize),
		boxType:    bi.Type.String(),
		supported:  bi.IsSupportedType(),
	}, nil
}

// supportedByMP4 reports whether go-mp4 knows the box type, which guards the
// sample-table unmarshalling path against unknown or typo'd boxes.
func supportedByMP4(boxType string) bool {
	h, err := readBoxHeaderString(boxType)
	if err != nil {
		return false
	}
	return h.supported
}

// readBoxHeaderString builds a minimal in-memory header for a type check.
func readBoxHeaderString(boxType string) (boxHeader, error) {
	if len(boxType) != 4 {
		return boxHeader{}, clock.NewOffsetError(0, clock.CategoryBounds, "bad box type", nil)
	}
	hdr := make([]byte, 8)
	for i := 0; i < 4; i++ {
		hdr[4+i] = boxType[i]
	}
	// A self-consistent 8-byte box with no payload.
	for i := 0; i < 4; i++ {
		hdr[i] = 0
	}
	hdr[0] = 0
	hdr[3] = 8
	bi, err := mp4.ReadBoxInfo(bytes.NewReader(hdr))
	if err != nil {
		return boxHeader{}, err
	}
	return boxHeader{
		size: int64(bi.Size), headerSize: int64(bi.HeaderSize),
		boxType: bi.Type.String(), supported: bi.IsSupportedType(),
	}, nil
}
