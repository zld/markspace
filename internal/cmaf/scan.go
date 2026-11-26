// Package cmaf scans bounded ISOBMFF/CMAF boxes and extracts fragment timing.
package cmaf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"example.com/archiveverify/internal/model"
)

// MaxBoxSize is the hard box-size admission bound.
const MaxBoxSize int64 = 64 << 20

// BoxHeader is the provenance and bounds information for one box.
type BoxHeader struct {
	Type          string `json:"type"`
	Offset        int64  `json:"offset"`
	Size          int64  `json:"size"`
	HeaderSize    int64  `json:"header_size"`
	PayloadOffset int64  `json:"payload_offset"`
	PayloadSize   int64  `json:"payload_size"`
	End           int64  `json:"end"`
}

// ReadBoxAt reads one box header without seeking. r must be at least size
// bytes long. The returned error retains source and offset provenance.
func ReadBoxAt(r io.ReaderAt, source string, offset, size, maxBox int64) (BoxHeader, error) {
	if offset < 0 || size < 0 || offset > size {
		return BoxHeader{}, model.NewParseError(source, "", offset, offset,
			fmt.Errorf("invalid box range offset=%d size=%d", offset, size))
	}
	if offset == size {
		return BoxHeader{}, io.EOF
	}

	var first [8]byte
	n, err := r.ReadAt(first[:], offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return BoxHeader{}, model.NewParseError(source, "", offset, offset+int64(n), err)
	}
	if n < 8 {
		return BoxHeader{}, model.NewParseError(source, "", offset, offset+int64(n),
			io.ErrUnexpectedEOF)
	}

	typ := string(first[4:8])
	headerSize := int64(8)
	declared := int64(binary.BigEndian.Uint32(first[0:4]))
	boxEnd := int64(0)

	switch declared {
	case 1:
		headerSize = 16
		var ext [8]byte
		n, err := r.ReadAt(ext[:], offset+8)
		if err != nil && !errors.Is(err, io.EOF) {
			return BoxHeader{}, model.NewParseError(source, typ, offset, offset+8+int64(n), err)
		}
		if n < 8 {
			return BoxHeader{}, model.NewParseError(source, typ, offset, offset+8+int64(n),
				io.ErrUnexpectedEOF)
		}
		declared = int64(binary.BigEndian.Uint64(ext[:]))
		if declared < headerSize {
			return BoxHeader{}, model.NewParseError(source, typ, offset, offset,
				fmt.Errorf("large box size %d shorter than header", declared))
		}
		if declared > math.MaxInt64-offset {
			return BoxHeader{}, model.NewParseError(source, typ, offset, math.MaxInt64,
				fmt.Errorf("box end overflow"))
		}
		boxEnd = offset + declared
	case 0:
		declared = size - offset
		boxEnd = size
	default:
		if declared < headerSize {
			return BoxHeader{}, model.NewParseError(source, typ, offset, offset,
				fmt.Errorf("box size %d shorter than header", declared))
		}
		if declared > math.MaxInt64-offset {
			return BoxHeader{}, model.NewParseError(source, typ, offset, math.MaxInt64,
				fmt.Errorf("box end overflow"))
		}
		boxEnd = offset + declared
	}

	if boxEnd > size {
		return BoxHeader{}, model.NewParseError(source, typ, offset, size,
			fmt.Errorf("%s box ends at %d beyond input size %d", typ, boxEnd, size))
	}
	actualSize := boxEnd - offset
	if maxBox > 0 && actualSize > maxBox {
		me := model.NewParseError(source, typ, offset, offset,
			fmt.Errorf("%s box size %d exceeds limit %d", typ, actualSize, maxBox))
		me.Code = model.CodeLimit
		me.Err = errors.Join(model.ErrResourceLimit, me.Err)
		return BoxHeader{}, me
	}

	return BoxHeader{
		Type:          typ,
		Offset:        offset,
		Size:          actualSize,
		HeaderSize:    headerSize,
		PayloadOffset: offset + headerSize,
		PayloadSize:   actualSize - headerSize,
		End:           boxEnd,
	}, nil
}

// ScanBoxes scans a complete bounded byte range, returning top-level boxes.
func ScanBoxes(r io.ReaderAt, source string, size, maxBox int64) ([]BoxHeader, error) {
	return ScanRange(r, source, 0, size, maxBox)
}

// ScanRange scans boxes contained in [start,end). It is used for both
// top-level files and bounded container payloads.
func ScanRange(r io.ReaderAt, source string, start, end, maxBox int64) ([]BoxHeader, error) {
	if start < 0 || end < start || end > math.MaxInt64 {
		return nil, model.NewParseError(source, "", start, start,
			fmt.Errorf("invalid scan range [%d,%d)", start, end))
	}
	var out []BoxHeader
	pos := start
	for pos < end {
		h, err := ReadBoxAt(r, source, pos, end, maxBox)
		if err != nil {
			return out, err
		}
		out = append(out, h)
		if h.End <= pos {
			return out, model.NewParseError(source, h.Type, h.Offset, h.Offset,
				errors.New("non-advancing box"))
		}
		pos = h.End
	}
	return out, nil
}
