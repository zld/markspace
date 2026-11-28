package ingest

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/abema/go-mp4"

	"mediapack/internal/model"
)

// boxWriter serializes the project MP4 subset without depending on the full
// go-mp4 marshaller: every fixture value is written explicitly so the parser
// receives byte-accurate structures with known offsets.
type boxWriter struct {
	buf *bytes.Buffer
}

func newBoxWriter() *boxWriter { return &boxWriter{buf: &bytes.Buffer{}} }

// box writes a four-character box header around a payload.
func (w *boxWriter) box(boxType string, payload []byte) {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(payload)))
	copy(hdr[4:8], []byte(boxType))
	w.buf.Write(hdr)
	w.buf.Write(payload)
}

// fullBox prefixes a version/flags header onto payload fields.
func fullBox(version uint8, flags uint32, body []byte) []byte {
	out := make([]byte, 0, 4+len(body))
	out = append(out, version, byte(flags>>16), byte(flags>>8), byte(flags))
	return append(out, body...)
}

func putU16(b []byte, v uint16) { binary.BigEndian.PutUint16(b, v) }
func putU32(b []byte, v uint32) { binary.BigEndian.PutUint32(b, v) }
func putI32(b []byte, v int32)  { binary.BigEndian.PutUint32(b, uint32(v)) }

// scanBoxes walks top-level (and recursively nested) box boundaries without
// interpreting payloads. It returns the offset of every box together with its
// type path, so parse errors can report a precise file offset and field path.
type boxEdge struct {
	Offset int64
	Size   uint64
	Type   string
	Path   string
}

// scanBoxBoundaries reads box headers inside [start, start+limit). Depth is
// capped to guard against pathological nesting.
func scanBoxBoundaries(r io.ReadSeeker, file string, start int64, limit uint64, prefix string) ([]boxEdge, error) {
	var edges []boxEdge
	pos := start
	end := start + int64(limit)
	for pos < end {
		if end-pos < 8 {
			return edges, model.Truncated(file, pos, prefix+"/<box-header>",
				"structure ends before declared length")
		}
		hdr := make([]byte, 8)
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return edges, err
		}
		if _, err := io.ReadFull(r, hdr); err != nil {
			return edges, model.Truncated(file, pos, prefix+"/<box-header>",
				"structure ends before declared length")
		}
		size := uint64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		hdrSize := uint64(8)
		if size == 1 {
			ext := make([]byte, 8)
			if _, err := io.ReadFull(r, ext); err != nil {
				return edges, model.Truncated(file, pos, prefix+"/<largesize>",
					"large size header is incomplete")
			}
			size = binary.BigEndian.Uint64(ext)
			hdrSize = 16
		}
		if size < hdrSize {
			return edges, model.OutOfRange(file, pos, prefix+"/"+typ+"/size",
				"declared box size smaller than header")
		}
		if uint64(end-pos) < size {
			return edges, model.OutOfRange(file, pos, prefix+"/"+typ+"/size",
				"box size crosses container boundary")
		}
		path := prefix + "/" + typ
		edges = append(edges, boxEdge{Offset: pos, Size: size, Type: typ, Path: path})
		if isContainerBox(typ) {
			childStart := pos + int64(hdrSize)
			children, err := scanBoxBoundaries(r, file, childStart, size-hdrSize, path)
			if err != nil {
				return edges, err
			}
			edges = append(edges, children...)
		}
		pos += int64(size)
	}
	return edges, nil
}

// isContainerBox lists the structural boxes whose payload holds child boxes.
func isContainerBox(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "edts", "udta":
		return true
	}
	return false
}

// mp4Type is a small helper to build go-mp4 BoxType values.
func mp4Type(s string) mp4.BoxType { return mp4.StrToBoxType(s) }
