package scan

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
)

// boxAt reads one box header at off without consuming the file position.
func (s *Scanner) boxAt(off int64) (*mp4.BoxInfo, error) {
	r := bytes.NewReader(s.data)
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	bi, err := mp4.ReadBoxInfo(r)
	if err != nil {
		return nil, err
	}
	return bi, nil
}

// walkChildren iterates the direct children of a container in [off, end).
func (s *Scanner) walkChildren(off, end int64, fn func(bi *mp4.BoxInfo) bool) bool {
	for off < end {
		bi, err := s.boxAt(off)
		if err != nil {
			s.fail(fault.Wrap(fault.StageScan, "scan.box.header", off, clock.Invalid,
				"child box header cannot be read", err))
			return false
		}
		if bi.Size < bi.HeaderSize || int64(bi.Size) > end-off {
			s.fail(fault.New(fault.StageScan, "scan.box.truncated", off, clock.Invalid,
				fmt.Sprintf("%s box declares %d bytes but only %d remain in its parent", bi.Type, bi.Size, end-off)))
			return false
		}
		if !fn(bi) {
			return false
		}
		off += int64(bi.Size)
	}
	if off != end {
		s.fail(fault.New(fault.StageScan, "scan.box.overrun", end, clock.Invalid,
			"child boxes do not fill their parent exactly"))
		return false
	}
	return true
}

// decode unmarshals the payload of bi into dst.
func (s *Scanner) decode(bi *mp4.BoxInfo, dst mp4.IBox) error {
	r := bytes.NewReader(s.data)
	if _, err := bi.SeekToPayload(r); err != nil {
		return err
	}
	_, err := mp4.Unmarshal(r, bi.Size-bi.HeaderSize, dst, mp4.Context{})
	return err
}

// parseMoov registers every track declared in moov.

// headerSize returns the encoded box header length at off (8 or 16).
func headerSize(data []byte, off int64) int64 {
	if off+8 > int64(len(data)) {
		return 8
	}
	if binary.BigEndian.Uint32(data[off:off+4]) == 1 {
		return 16
	}
	return 8
}
