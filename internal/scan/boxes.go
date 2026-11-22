// Package scan turns MP4 metadata plus a manifest sidecar into bounded,
// per-track sample iterators. It never decodes payloads and never loads a
// whole sample table into memory: tables are validated against the file size
// first and then walked with table cursors in fixed-size batches.
package scan

import (
	"encoding/binary"
	"io"

	"mediacore/internal/clock"
)

// MaxBoxScanBytes bounds the total bytes of box structure the validator is
// willing to walk on a single file. A file that claims more is rejected with
// a recoverable error rather than exhausting memory or disk seeks.
const MaxBoxScanBytes uint64 = 8 << 30 // 8 GiB

// BoxHeader describes one validated MP4 container box.
type BoxHeader struct {
	Type       [4]byte
	Offset     uint64 // absolute offset of the box header in the file
	HeaderSize uint64 // 8 (32-bit size) or 16 (64-bit largesize)
	Size       uint64 // total box size including the header
	Payload    uint64 // Size - HeaderSize
}

// End returns the absolute offset just past the box.
func (b BoxHeader) End() uint64 { return b.Offset + b.Size }

// WalkBoxes performs a depth-first, single-pass structural validation of the
// MP4 box hierarchy. Every declared size is checked against fileSize and
// against uint64 overflow before the callback is invoked, so malformed input
// can never drive an out-of-bounds seek. The callback may return
// ErrStopScan to stop early without that being an error.
func WalkBoxes(r io.ReaderAt, fileSize uint64, visit func(BoxHeader, int) error) error {
	return walkLevel(r, 0, fileSize, 0, visit)
}

func walkLevel(r io.ReaderAt, start, end uint64, depth int, visit func(BoxHeader, int) error) error {
	off := start
	for off+8 <= end {
		hdr, err := readHeader(r, off, end)
		if err != nil {
			return err
		}
		if err := visit(hdr, depth); err != nil {
			if err == ErrStopScan {
				return nil
			}
			return err
		}
		next := hdr.End()
		if next > end {
			return clock.ErrRecoverablef("scan.box", "box %q at offset %d ends beyond container", hdr.Type, hdr.Offset)
		}
		if isContainer(hdr.Type) && hdr.Payload > 0 {
			// FullBox containers reserve 4 version/flags bytes before children.
			childStart := off + hdr.HeaderSize
			if hdr.Type == boxType("meta") {
				childStart += 4
			}
			if err := walkLevel(r, childStart, next, depth+1, visit); err != nil {
				return err
			}
		}
		off = next
	}
	if off != end {
		return clock.ErrRecoverablef("scan.box", "trailing %d bytes not aligned to a box boundary", end-off)
	}
	return nil
}

func readHeader(r io.ReaderAt, off, end uint64) (BoxHeader, error) {
	var raw [16]byte
	if _, err := r.ReadAt(raw[:8], int64(off)); err != nil {
		return BoxHeader{}, clock.ErrRecoverablef("scan.box", "truncated box header at offset %d: %v", off, err)
	}
	size32 := binary.BigEndian.Uint32(raw[0:4])
	var hdr BoxHeader
	hdr.Offset = off
	copy(hdr.Type[:], raw[4:8])
	hdr.HeaderSize = 8
	switch size32 {
	case 0: // box extends to end of file
		hdr.Size = end - off
	case 1: // 64-bit largesize
		if off+16 > end {
			return BoxHeader{}, clock.ErrRecoverablef("scan.box", "truncated 64-bit box header at offset %d", off)
		}
		if _, err := r.ReadAt(raw[8:16], int64(off+8)); err != nil {
			return BoxHeader{}, clock.ErrRecoverablef("scan.box", "truncated largesize at offset %d", off)
		}
		hdr.Size = binary.BigEndian.Uint64(raw[8:16])
		hdr.HeaderSize = 16
	default:
		hdr.Size = uint64(size32)
	}
	if hdr.Size < hdr.HeaderSize {
		return BoxHeader{}, clock.ErrRecoverablef("scan.box", "box %q at offset %d has illegal size %d", hdr.Type, off, hdr.Size)
	}
	if hdr.End() < off { // uint64 overflow
		return BoxHeader{}, clock.ErrOverflow("scan.box")
	}
	if hdr.End() > end {
		return BoxHeader{}, clock.ErrRecoverablef("scan.box", "box %q at offset %d size %d exceeds container end %d", hdr.Type, off, hdr.Size, end)
	}
	hdr.Payload = hdr.Size - hdr.HeaderSize
	return hdr, nil
}

func boxType(s string) [4]byte {
	var t [4]byte
	copy(t[:], s)
	return t
}

func isContainer(t [4]byte) bool {
	switch string(t[:]) {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "dinf", "udta", "meta":
		return true
	}
	return false
}

// ErrStopScan terminates WalkBoxes early without an error.
var ErrStopScan = clock.ErrRecoverablef("scan.box", "stop")
