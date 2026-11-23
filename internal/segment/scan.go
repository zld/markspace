// Package segment scans ISO BMFF fragment files (styp/moof/mdat sequences)
// without loading the whole input into memory. It reports segment
// boundaries, sample counts, and structured diagnostics for truncated
// boxes, illegal sizes, incomplete sample tables and missing indexes. On a
// recoverable failure the scanner resynchronises at the next top-level box
// boundary and keeps going.
package segment

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"mediadeliver/internal/model"
)

// Box types the scanner understands.
const (
	boxStyp = "styp"
	boxMoof = "moof"
	boxMdat = "mdat"
	boxMfra = "mfra"
	boxSidx = "sidx"
)

// Result is the scan outcome for one input file.
type Result struct {
	Path        string         `json:"path"`
	Size        int64          `json:"size"`
	Segments    []Boundary     `json:"segments"`
	Errors      []*model.Error `json:"errors,omitempty"`
	ScannedTo   int64          `json:"scanned_to"`
	IndexFound  bool           `json:"index_found"`
	SampleCount int64          `json:"sample_count"`
}

// Boundary is one complete media segment (moof + following mdat).
type Boundary struct {
	Index          int    `json:"index"`
	Offset         int64  `json:"offset"`
	Size           int64  `json:"size"`
	MoofOffset     int64  `json:"moof_offset"`
	MdatOffset     int64  `json:"mdat_offset"`
	MdatSize       int64  `json:"mdat_size"`
	SampleCount    uint32 `json:"sample_count"`
	TrackID        uint32 `json:"track_id"`
	BaseDecodeTime uint64 `json:"base_decode_time"`
	DurationTicks  uint64 `json:"duration_ticks"`
	Corrupt        bool   `json:"corrupt"`
	ErrorCode      string `json:"error_code,omitempty"`
}

// boxHeader is a parsed top-level box header.
type boxHeader struct {
	offset     int64
	size       int64
	headerSize int64
	typ        string
	extendEOF  bool
}

type scanner struct {
	r        io.ReadSeeker
	fileSize int64
	res      *Result
	cur      *Boundary
	segIdx   int
}

// ScanFile scans path. A nil error indicates the file was readable; all
// structural damage is reported in Result.Errors.
func ScanFile(path string) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.Wrap(err, model.CodeTruncatedBox, path, model.NoOffset, model.StageScan, false)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, model.Wrap(err, model.CodeTruncatedBox, path, model.NoOffset, model.StageScan, false)
	}
	return Scan(f, path, st.Size())
}

// Scan scans an already-open input of the given size.
func Scan(r io.ReadSeeker, path string, size int64) (*Result, error) {
	s := &scanner{r: r, fileSize: size, res: &Result{Path: path, Size: size}}
	if err := s.scan(); err != nil {
		return nil, err
	}
	s.flushSegment()
	model.SortErrors(s.res.Errors)
	return s.res, nil
}

func (s *scanner) scan() error {
	pos := int64(0)
	for pos < s.fileSize {
		hdr, err := s.readHeader(pos)
		if err != nil {
			// The header itself is truncated or illegal. Nothing after this
			// point can be trusted, so scanning stops here.
			s.res.ScannedTo = pos
			return nil
		}
		if hdr.size < hdr.headerSize {
			s.res.Errors = append(s.res.Errors, model.NewError(model.CodeInvalidBoxSize, s.res.Path,
				pos, model.StageScan, true,
				fmt.Sprintf("box %q declares size %d below its %d-byte header", hdr.typ, hdr.size, hdr.headerSize)))
			s.res.ScannedTo = pos
			return nil
		}
		end := hdr.offset + hdr.size
		if end > s.fileSize {
			// A top-level box can never be larger than its containing file.
			// If it claims to be, the length field itself is illegal;
			// otherwise the recording was truncated mid-write.
			code := model.CodeTruncatedBox
			msg := fmt.Sprintf("box %q needs %d bytes but only %d remain", hdr.typ, hdr.size, s.fileSize-hdr.offset)
			if hdr.size > s.fileSize {
				code = model.CodeInvalidBoxSize
				msg = fmt.Sprintf("box %q declares %d bytes in a %d byte file", hdr.typ, hdr.size, s.fileSize)
			}
			s.res.Errors = append(s.res.Errors, model.NewError(code, s.res.Path,
				hdr.offset, model.StageScan, true, msg))
			if hdr.typ == boxMdat && s.cur != nil && code == model.CodeTruncatedBox {
				s.cur.MdatSize = s.fileSize - hdr.offset
				s.cur.Corrupt = true
				s.cur.ErrorCode = string(model.CodeTruncatedBox)
				s.flushSegment()
			} else if s.cur != nil {
				s.cur.Corrupt = true
				s.cur.ErrorCode = string(code)
				s.flushSegment()
			}
			s.res.ScannedTo = hdr.offset
			return nil
		}
		switch hdr.typ {
		case boxStyp:
			s.flushSegment()
		case boxSidx:
			s.res.IndexFound = true
			s.flushSegment()
		case boxMoof:
			s.flushSegment()
			s.cur = &Boundary{Index: s.segIdx, Offset: hdr.offset, MoofOffset: hdr.offset}
			if err := s.readMoof(hdr); err != nil {
				s.res.Errors = append(s.res.Errors, model.Wrap(err, model.CodeTruncatedBox, s.res.Path,
					hdr.offset, model.StageScan, true))
				s.res.ScannedTo = pos
				return nil
			}
		case boxMdat:
			if s.cur == nil {
				// Free-standing mdat (or a moof that failed to parse): note
				// it and continue at the next boundary.
				s.res.Errors = append(s.res.Errors, model.NewError(model.CodeIncompleteSamples, s.res.Path,
					hdr.offset, model.StageScan, true, "mdat without a preceding moof"))
			} else {
				s.cur.MdatOffset = hdr.offset
				s.cur.MdatSize = hdr.size
				s.cur.Size = end - s.cur.Offset
				s.flushSegment()
			}
		case boxMfra:
			s.flushSegment()
			s.res.IndexFound = true
		default:
			// styp/free/skip/prft and friends are opaque here.
		}
		pos = end
	}
	s.res.ScannedTo = s.fileSize
	return nil
}

func (s *scanner) flushSegment() {
	if s.cur == nil {
		return
	}
	if s.cur.Size == 0 {
		s.cur.Size = s.cur.MdatOffset - s.cur.Offset + s.cur.MdatSize
	}
	s.res.Segments = append(s.res.Segments, *s.cur)
	s.res.SampleCount += int64(s.cur.SampleCount)
	s.segIdx++
	s.cur = nil
}

// readHeader parses one top-level box header at pos.
func (s *scanner) readHeader(pos int64) (boxHeader, error) {
	hdr := boxHeader{offset: pos}
	if s.fileSize-pos < 8 {
		s.res.Errors = append(s.res.Errors, model.NewError(model.CodeTruncatedBox, s.res.Path,
			pos, model.StageScan, true,
			fmt.Sprintf("only %d bytes remain for a box header", s.fileSize-pos)))
		return hdr, io.ErrUnexpectedEOF
	}
	var buf [16]byte
	if _, err := s.r.Seek(pos, io.SeekStart); err != nil {
		return hdr, err
	}
	if _, err := io.ReadFull(s.r, buf[:8]); err != nil {
		return hdr, err
	}
	size32 := binary.BigEndian.Uint32(buf[:4])
	hdr.typ = string(buf[4:8])
	hdr.headerSize = 8
	switch size32 {
	case 0:
		hdr.size = s.fileSize - pos
		hdr.extendEOF = true
	case 1:
		if s.fileSize-pos < 16 {
			s.res.Errors = append(s.res.Errors, model.NewError(model.CodeTruncatedBox, s.res.Path,
				pos, model.StageScan, true, "64-bit box header is truncated"))
			return hdr, io.ErrUnexpectedEOF
		}
		if _, err := io.ReadFull(s.r, buf[8:16]); err != nil {
			return hdr, err
		}
		hdr.size = int64(binary.BigEndian.Uint64(buf[8:16]))
		hdr.headerSize = 16
		if hdr.size < 0 {
			s.res.Errors = append(s.res.Errors, model.NewError(model.CodeInvalidBoxSize, s.res.Path,
				pos, model.StageScan, true, "64-bit box size overflows int64"))
			return hdr, fmt.Errorf("invalid box size")
		}
	default:
		hdr.size = int64(size32)
	}
	return hdr, nil
}
