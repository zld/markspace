package media

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"replay/internal/clock"
)

// headerLen reads an 8/16 byte box header at the current position.
func headerLen(htype string, size32 uint32) int {
	if htype == "uuid" {
		return 16 + 16
	}
	if size32 == 1 {
		return 16
	}
	return 8
}

// scanTopLevel walks the top-level box chain manually so that a truncated
// header or payload is reported with the exact byte offset of the box.
func scanTopLevel(f *os.File) ([]BoxRef, BoxRef, BoxRef, error) {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, BoxRef{}, BoxRef{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, BoxRef{}, BoxRef{}, err
	}
	var refs []BoxRef
	var moov, mdat BoxRef
	pos := int64(0)
	hdr := make([]byte, 16)
	for pos < size {
		if _, err := io.ReadFull(f, hdr[:8]); err != nil {
			return nil, BoxRef{}, BoxRef{}, clock.NewError(clock.KindBoxTruncated,
				"scan", "box header truncated at EOF").WithOffset(pos)
		}
		size32 := binary.BigEndian.Uint32(hdr[0:4])
		htype := string(hdr[4:8])
		hlen := headerLen(htype, size32)
		if hlen == 16 {
			if _, err := io.ReadFull(f, hdr[8:16]); err != nil {
				return nil, BoxRef{}, BoxRef{}, clock.NewError(clock.KindBoxTruncated,
					"scan", "extended box header truncated").WithOffset(pos)
			}
		}
		var boxSize int64
		switch {
		case size32 == 1:
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
		case size32 == 0:
			boxSize = size - pos
		default:
			boxSize = int64(size32)
		}
		if boxSize < int64(hlen) {
			return nil, BoxRef{}, BoxRef{}, clock.NewError(clock.KindBoxMalformed,
				"scan", fmt.Sprintf("box %s size %d smaller than header", htype, boxSize)).WithOffset(pos)
		}
		if pos+boxSize > size {
			return nil, BoxRef{}, BoxRef{}, clock.NewError(clock.KindBoxTruncated,
				"scan", fmt.Sprintf("box %s payload runs past EOF", htype)).WithOffset(pos)
		}
		ref := BoxRef{Type: htype, Offset: pos, Size: boxSize, Header: int64(hlen)}
		refs = append(refs, ref)
		switch htype {
		case "moov":
			moov = ref
		case "mdat":
			mdat = ref
		}
		pos += boxSize
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, BoxRef{}, BoxRef{}, err
		}
	}
	return refs, moov, mdat, nil
}
