// Package mp4 inspects MP4/ISOBMFF files using github.com/abema/go-mp4 for
// typed box parsing, augmented with a fault-tolerant header walker that
// pins illegal lengths and truncation to exact byte offsets.
package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"mediascan/internal/model"
)

// BoxHeader is the located header of one box.
type BoxHeader struct {
	Type          string
	Path          string
	Offset        int64
	HeaderSize    int64
	PayloadOffset int64
	Size          int64 // resolved total size; file-end for size==0
	Depth         int
}

func (h BoxHeader) payloadEnd(fileSize int64) int64 {
	if h.Size <= 0 {
		return fileSize
	}
	return h.Offset + h.Size
}

// containerTypes are boxes whose payload is a sequence of child boxes.
var containerTypes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "dinf": true, "edts": true, "udta": true,
	"mvex": true, "moof": true, "traf": true, "mfra": true,
}

// scanBoxes walks the box hierarchy within [start,end). It returns located
// headers and model faults for illegal lengths and truncation. The walk is
// deliberately defensive: one bad box is reported and contained rather than
// aborting the whole file.
func scanBoxes(f *os.File, fileSize int64, segment string) ([]BoxHeader, []model.Fault) {
	var headers []BoxHeader
	var faults []model.Fault

	var readAt = func(off int64, n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := f.ReadAt(b, off)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}
		if err != nil {
			return b, err
		}
		return b, nil
	}

	var walkRange func(start, end int64, depth int, prefix string)
	walkRange = func(start, end int64, depth int, prefix string) {
		off := start
		for off+8 <= end {
			raw, rerr := readAt(off, 8)
			if rerr != nil && len(raw) < 8 {
				faults = append(faults, truncFault(segment, "", off, fileSize,
					fmt.Sprintf("short box header at offset %d: %v", off, rerr)))
				return
			}
			size32 := binary.BigEndian.Uint32(raw[0:4])
			typ := string(raw[4:8])
			headerSize := int64(8)
			boxSize := int64(size32)

			if size32 == 1 {
				ext, err := readAt(off+8, 8)
				if err != nil || len(ext) < 8 {
					faults = append(faults, truncFault(segment, typ, off, fileSize,
						"64-bit box size header is truncated"))
					return
				}
				headerSize = 16
				boxSize = int64(binary.BigEndian.Uint64(ext))
				if boxSize < 16 {
					faults = append(faults, illegalFault(segment, typ, off,
						fmt.Sprintf("illegal largesize %d (< 16)", boxSize)))
					return
				}
			} else if size32 == 0 {
				boxSize = end - off // extends to enclosing/ file end
			} else if boxSize < 8 {
				faults = append(faults, illegalFault(segment, typ, off,
					fmt.Sprintf("illegal box size %d (< 8)", boxSize)))
				return
			}

			if off+headerSize > fileSize {
				faults = append(faults, truncFault(segment, typ, off, fileSize, "box header extends past EOF"))
				return
			}

			declaredEnd := off + boxSize
			h := BoxHeader{
				Type: typ, Path: prefix + "/" + typ, Offset: off,
				HeaderSize: headerSize, PayloadOffset: off + headerSize,
				Size: boxSize, Depth: depth,
			}

			if boxSize > 0 && declaredEnd > fileSize {
				faults = append(faults, truncFault(segment, typ, off, fileSize,
					fmt.Sprintf("box %q declares end %d but file is %d bytes", typ, declaredEnd, fileSize)))
				headers = append(headers, h)
				return
			}
			if boxSize > 0 && declaredEnd > end {
				faults = append(faults, illegalFault(segment, typ, off,
					fmt.Sprintf("box %q size %d overruns enclosing box end %d", typ, boxSize, end)))
				headers = append(headers, h)
				return
			}

			headers = append(headers, h)
			if containerTypes[typ] && declaredEnd > off+headerSize {
				childEnd := declaredEnd
				if size32 == 0 {
					childEnd = end
				}
				walkRange(off+headerSize, childEnd, depth+1, h.Path)
			}
			if size32 == 0 {
				return
			}
			off = declaredEnd
		}
		// 1..7 trailing bytes mean a box header was cut off.
		if off < end && end-off < 8 {
			faults = append(faults, truncFault(segment, "", off, fileSize,
				fmt.Sprintf("trailing %d byte(s) form an incomplete box header", end-off)))
		}
	}

	walkRange(0, fileSize, 0, "")
	return headers, faults
}

func truncFault(segment, typ string, boxOffset, eof int64, msg string) model.Fault {
	f := model.NewFault(model.CodeTruncatedBox, segment, msg)
	f.BoxType = typ
	f.BoxOffset = boxOffset
	f.OffsetBytes = eof // exact cut point = actual EOF
	f.SampleIndex = model.Unknown
	f.TimeStart, f.TimeEnd = model.Unknown, model.Unknown
	return f
}

func illegalFault(segment, typ string, boxOffset int64, msg string) model.Fault {
	f := model.NewFault(model.CodeIllegalBoxLength, segment, msg)
	f.BoxType = typ
	f.BoxOffset = boxOffset
	f.OffsetBytes = boxOffset
	f.SampleIndex = model.Unknown
	f.TimeStart, f.TimeEnd = model.Unknown, model.Unknown
	return f
}

// findHeader returns the first header of the given type.
func findHeader(hs []BoxHeader, typ string) *BoxHeader {
	for i := range hs {
		if hs[i].Type == typ {
			return &hs[i]
		}
	}
	return nil
}

// findAllHeaders returns all headers of the given type in file order.
func findAllHeaders(hs []BoxHeader, typ string) []BoxHeader {
	var out []BoxHeader
	for i := range hs {
		if hs[i].Type == typ {
			out = append(out, hs[i])
		}
	}
	return out
}
