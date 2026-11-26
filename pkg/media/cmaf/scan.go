// Package cmaf parses and repairs fragmented MP4 (CMAF) objects.
//
// Scanning is streaming and bounded: every read goes through a limit-aware
// reader anchored at the actual file size, so a malformed length field can
// never make the parser touch bytes beyond the end of the file. Structural
// defects (bad lengths, mfhd gaps/duplicates, tfdt rollback, mdat
// truncation) are reported as timebase.MediaError values carrying the
// representation, fragment, box and absolute byte offset.
package cmaf

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"cmafrepair/pkg/media/timebase"
)

// Box names used across the scanner.
const (
	BoxFtyp = "ftyp"
	BoxMoov = "moov"
	BoxMoof = "moof"
	BoxMdat = "mdat"
	BoxMfhd = "mfhd"
	BoxTraf = "traf"
	BoxTfhd = "tfhd"
	BoxTfdt = "tfdt"
	BoxTrun = "trun"
	BoxFree = "free"
	BoxSkip = "skip"
)

// BoxHeader describes one ISO BMFF box as found on disk.
type BoxHeader struct {
	Type       string // four-character box type
	Offset     int64  // absolute offset of the size field
	HeaderSize int64  // 8 for 32-bit header, 16 for 64-bit largesize
	Size       int64  // total box size including the header; -1 when box extends to EOF
	PayloadOff int64  // absolute offset of the first payload byte
	PayloadLen int64  // payload bytes; -1 when the box extends to EOF
}

// End returns the absolute offset immediately after the box. For EOF boxes
// it returns declaredEOF.
func (b BoxHeader) End(declaredEOF int64) int64 {
	if b.Size < 0 {
		return declaredEOF
	}
	return b.Offset + b.Size
}

// Size32 is the classic compact header: size(32) + type(32).
const Size32 = 8

// Size64 is the largesize header: size(32)=1 + type(32) + largesize(64).
const Size64 = 16

// ErrStopScan ends a top-level scan loop without reporting a defect.
var ErrStopScan = errors.New("cmaf: scan stopped")

// boundedReader fails any read past the declared end of the file instead of
// silently returning io.EOF. That distinction is what lets callers tell a
// truncated box (length promised more bytes than the file holds) from a
// clean end of input.
type boundedReader struct {
	r       io.Reader
	pos     int64
	end     int64
	zeroEOF bool // true once pos == end: next Read returns io.EOF
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.pos >= b.end {
		b.zeroEOF = true
		return 0, io.EOF
	}
	remain := b.end - b.pos
	if int64(len(p)) > remain {
		p = p[:remain]
	}
	n, err := b.r.Read(p)
	b.pos += int64(n)
	if err == io.EOF && b.pos < b.end {
		// Underlying source ended before the declared file end.
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

// Scanner walks top-level boxes of a fragment/init file.
type Scanner struct {
	br      *boundedReader
	fileLen int64
	pos     int64
	hdr     BoxHeader
	err     error
	defects []*timebase.MediaError
	// corrupt marks the current box as extending past EOF; payload reads
	// against it are clamped to the physically available window.
	corrupt    bool
	corruptGap int64
}

// NewScanner wraps r, which must contain exactly fileLen bytes.
func NewScanner(r io.Reader, fileLen int64) *Scanner {
	return &Scanner{
		br:      &boundedReader{r: bufio.NewReaderSize(r, 64*1024), end: fileLen},
		fileLen: fileLen,
	}
}

// Next advances to the next top-level box. It returns false at clean EOF or
// when a hard defect has been recorded; call Err() to distinguish.
func (s *Scanner) Next() bool {
	if s.err != nil || s.pos >= s.fileLen {
		return false
	}
	hdr, err := readBoxHeader(s.br, s.pos, s.fileLen)
	if err != nil {
		s.err = err
		return false
	}
	s.corrupt = false
	s.corruptGap = 0
	if hdr.Size >= 0 {
		if hdr.Size < hdr.HeaderSize {
			s.err = timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, hdr.Offset,
				"box %s size %d smaller than header", hdr.Type, hdr.Size)
			return false
		}
		if hdr.Offset+hdr.Size > s.fileLen {
			gap := hdr.Offset + hdr.Size - s.fileLen
			s.defects = append(s.defects, timebase.AtError(timebase.CodeBoxLength,
				timebase.PhaseScan, hdr.Offset,
				"box %s declares %d bytes but only %d remain (%d missing)",
				hdr.Type, hdr.Size, s.fileLen-hdr.Offset, gap))
			s.corrupt = true
			s.corruptGap = gap
			s.pos = s.fileLen
		} else {
			s.pos = hdr.Offset + hdr.Size
		}
	} else {
		s.pos = s.fileLen
	}
	s.hdr = hdr
	return true
}

// Header returns the current box header.
func (s *Scanner) Header() BoxHeader { return s.hdr }

// Corrupt reports whether the current box claims bytes past EOF and, when
// so, how many payload bytes are missing.
func (s *Scanner) Corrupt() (bool, int64) { return s.corrupt, s.corruptGap }

// Defects returns non-fatal structural defects accumulated while scanning.
func (s *Scanner) Defects() []*timebase.MediaError { return s.defects }

// Skip discards the payload of the current box so the scanner stays aligned.
// For a box extending past EOF only the physically present bytes are drained.
func (s *Scanner) Skip() error {
	available := s.availablePayload()
	if _, err := io.CopyN(io.Discard, s.br, available); err != nil {
		return timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, s.hdr.PayloadOff,
			"cannot skip %s payload: %v", s.hdr.Type, err)
	}
	return nil
}

// availablePayload returns how many payload bytes physically exist.
func (s *Scanner) availablePayload() int64 {
	n := s.fileLen - s.hdr.PayloadOff
	if n < 0 {
		return 0
	}
	if s.hdr.PayloadLen >= 0 && s.hdr.PayloadLen < n {
		n = s.hdr.PayloadLen
	}
	return n
}

// Err returns the first defect encountered while scanning.
func (s *Scanner) Err() error { return s.err }

// ReadPayload reads at most max bytes of the current box payload. A max <= 0
// means the whole (physically available) payload. Reads never pass the
// declared box boundary or the physical end of file.
func (s *Scanner) ReadPayload(max int64) ([]byte, error) {
	h := s.hdr
	n := s.availablePayload()
	if max > 0 && max < n {
		n = max
	}
	if n > 64*1024*1024 {
		return nil, timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, h.PayloadOff,
			"box %s payload %d exceeds 64 MiB processing cap", h.Type, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(s.br, buf); err != nil {
		return nil, timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, h.PayloadOff,
			"short read of %s payload: %v", h.Type, err)
	}
	return buf, nil
}

// readBoxHeader parses an 8- or 16-byte box header at off.
func readBoxHeader(r io.Reader, off, fileLen int64) (BoxHeader, error) {
	var base [8]byte
	if _, err := io.ReadFull(r, base[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return BoxHeader{}, timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, off,
				"truncated box header at end of file")
		}
		return BoxHeader{}, timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, off, "%v", err)
	}
	rawSize := int64(binary.BigEndian.Uint32(base[0:4]))
	typ := string(base[4:8])
	hdr := BoxHeader{Type: typ, Offset: off, HeaderSize: Size32}
	switch rawSize {
	case 0:
		// Box extends to end of file.
		hdr.Size = -1
		hdr.PayloadOff = off + Size32
		hdr.PayloadLen = -1
	case 1:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return BoxHeader{}, timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, off+8,
				"truncated largesize header: %v", err)
		}
		large := int64(binary.BigEndian.Uint64(ext[:]))
		hdr.HeaderSize = Size64
		hdr.Size = large
		hdr.PayloadOff = off + Size64
		hdr.PayloadLen = large - Size64
	default:
		hdr.Size = rawSize
		hdr.PayloadOff = off + Size32
		hdr.PayloadLen = rawSize - Size32
	}
	if hdr.Size >= 0 && hdr.Offset+hdr.Size > fileLen {
		// Caller (Next) turns this into a positioned length error.
		return hdr, nil
	}
	return hdr, nil
}

// ScanTopLevel executes fn for every top-level box of r. The reader position
// is fully driven by the scanner; fn must consume boxes through the scanner.
func ScanTopLevel(r io.Reader, fileLen int64, fn func(*Scanner, BoxHeader) error) error {
	s := NewScanner(r, fileLen)
	for s.Next() {
		if err := fn(s, s.Header()); err != nil {
			if errors.Is(err, ErrStopScan) {
				return nil
			}
			return err
		}
	}
	return s.Err()
}

// childBox describes a box found while walking a container payload.
type childBox struct {
	hdr     BoxHeader
	payload []byte
}

// parseContainer walks immediate children of a container payload. off is the
// absolute file offset at which payload starts, so child offsets stay
// absolute. It never reads beyond payload.
func parseContainer(payload []byte, off int64) ([]childBox, error) {
	var out []childBox
	pos := int64(0)
	for pos < int64(len(payload)) {
		if int64(len(payload))-pos < 8 {
			return nil, timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, off+pos,
				"container has %d trailing bytes", int64(len(payload))-pos)
		}
		rawSize := int64(binary.BigEndian.Uint32(payload[pos : pos+4]))
		typ := string(payload[pos+4 : pos+8])
		hdr := BoxHeader{Type: typ, Offset: off + pos, HeaderSize: Size32}
		bodyStart := pos + 8
		switch rawSize {
		case 0:
			hdr.Size = -1
			hdr.PayloadOff = off + bodyStart
			hdr.PayloadLen = -1
			rawSize = int64(len(payload)) - pos
		case 1:
			if int64(len(payload))-pos < 16 {
				return nil, timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, off+pos,
					"truncated largesize child box")
			}
			large := int64(binary.BigEndian.Uint64(payload[pos+8 : pos+16]))
			hdr.HeaderSize = Size64
			hdr.Size = large
			hdr.PayloadOff = off + pos + 16
			hdr.PayloadLen = large - 16
			rawSize = large
		default:
			hdr.Size = rawSize
			hdr.PayloadOff = off + bodyStart
			hdr.PayloadLen = rawSize - 8
		}
		if rawSize < hdr.HeaderSize || pos+rawSize > int64(len(payload)) {
			return nil, timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, hdr.Offset,
				"child box %s length %d exceeds container", typ, rawSize)
		}
		var body []byte
		if hdr.PayloadLen > 0 {
			body = payload[bodyStart : pos+rawSize]
		}
		out = append(out, childBox{hdr: hdr, payload: body})
		pos += rawSize
	}
	return out, nil
}

// findChild returns the first child of the given type, or nil.
func findChild(children []childBox, typ string) *childBox {
	for i := range children {
		if children[i].hdr.Type == typ {
			return &children[i]
		}
	}
	return nil
}

// String renders a header for diagnostics.
func (b BoxHeader) String() string {
	return fmt.Sprintf("%s@%d(size=%d)", b.Type, b.Offset, b.Size)
}
