// Package mp4 scans the constrained MJPEG/PCM MP4 subset and muxes deliveries.
package mp4

import (
	"encoding/binary"
	"io"
	"os"

	"replayctl/internal/fault"
)

// BoxRef is a located MP4 box header.
type BoxRef struct {
	Type       string
	Offset     int64
	HeaderSize int64
	Size       int64
}

// Sample is one located media sample. Index is zero-based in this Go API.
type Sample struct {
	Index    uint32
	Offset   int64
	Size     int64
	DTS      int64
	Duration uint32
	Sync     bool
}

// Track contains a parsed sample table.
type Track struct {
	ID        uint32
	Kind      string // "video" or "audio"
	Codec     string
	Timescale uint32
	Width     uint32
	Height    uint32
	Samples   []Sample
}

// Movie is the parsed subset of an MP4 file.
type Movie struct {
	Path      string
	Size      int64
	Timescale uint32
	Tracks    []*Track
}

// Track returns the first track of the requested kind.
func (m *Movie) Track(kind string) *Track {
	for _, t := range m.Tracks {
		if t.Kind == kind {
			return t
		}
	}
	return nil
}

type scanner struct {
	r    io.ReaderAt
	size int64
	path string
	mov  *Movie
}

// ScanFile opens path and parses its boxes and sample tables.
func ScanFile(path string) (*Movie, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.New(fault.IO, fault.StageScan, path, -1, -1, "open: %v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fault.New(fault.IO, fault.StageScan, path, -1, -1, "stat: %v", err)
	}
	return Scan(f, st.Size(), path)
}

// Scan parses an MP4 from r. size is the complete input length.
func Scan(r io.ReaderAt, size int64, path string) (*Movie, error) {
	if size < 0 {
		return nil, fault.New(fault.InvalidBox, fault.StageScan, path, -1, -1, "negative size")
	}
	s := &scanner{r: r, size: size, path: path, mov: &Movie{Path: path, Size: size}}
	if err := s.parseChildren(0, size, nil); err != nil {
		return nil, err
	}
	return s.mov, nil
}

func (s *scanner) fail(code fault.Code, off, tick int64, format string, args ...any) error {
	return fault.New(code, fault.StageScan, s.path, off, tick, format, args...)
}

func (s *scanner) readAt(off int64, p []byte) error {
	n, err := io.ReadFull(io.NewSectionReader(s.r, off, int64(len(p))), p)
	if err != nil {
		return s.fail(fault.TruncatedBox, off+int64(n), -1, "read %d bytes at %d: %v", len(p), off, err)
	}
	return nil
}

func fourCC(b []byte) string {
	for len(b) > 0 && b[len(b)-1] == ' ' {
		b = b[:len(b)-1]
	}
	return string(b)
}

func (s *scanner) headerAt(off, limit int64) (BoxRef, int64, error) {
	var h [16]byte
	if off >= s.size {
		return BoxRef{}, 0, s.fail(fault.TruncatedBox, s.size, -1, "missing box header")
	}
	want := int64(8)
	if off+want > s.size {
		want = s.size - off
	}
	if err := s.readAt(off, h[:want]); err != nil {
		return BoxRef{}, 0, err
	}
	if want < 8 {
		return BoxRef{}, 0, s.fail(fault.TruncatedBox, off+want, -1, "short box header")
	}
	size32 := binary.BigEndian.Uint32(h[0:4])
	typ := fourCC(h[4:8])
	headerSize := int64(8)
	size := int64(size32)
	if size32 == 1 {
		if err := s.readAt(off+8, h[8:16]); err != nil {
			return BoxRef{}, 0, err
		}
		size = int64(binary.BigEndian.Uint64(h[8:16]))
		headerSize = 16
	} else if size32 == 0 {
		size = limit - off
	}
	if size < headerSize {
		return BoxRef{}, 0, s.fail(fault.InvalidBox, off, -1, "box %q size %d < header %d", typ, size, headerSize)
	}
	end := off + size
	if end > limit {
		code := fault.InvalidBox
		offErr := off
		if end > s.size {
			code = fault.TruncatedBox
			offErr = s.size // first unreadable byte of the physical input
		}
		return BoxRef{}, 0, s.fail(code, offErr, -1, "box %q ends at %d beyond limit %d", typ, end, limit)
	}
	return BoxRef{Type: typ, Offset: off, HeaderSize: headerSize, Size: size}, end, nil
}

func (s *scanner) parseChildren(start, end int64, tb *trackBuilder) error {
	for off := start; off < end; {
		box, next, err := s.headerAt(off, end)
		if err != nil {
			return err
		}
		payload := box.Offset + box.HeaderSize
		switch box.Type {
		case "moov":
			if err := s.parseChildren(payload, next, nil); err != nil {
				return err
			}
		case "trak":
			b := &trackBuilder{}
			if err := s.parseChildren(payload, next, b); err != nil {
				return err
			}
			tr, err := b.build(s)
			if err != nil {
				return err
			}
			s.mov.Tracks = append(s.mov.Tracks, tr)
		case "mdia", "minf", "stbl":
			if err := s.parseChildren(payload, next, tb); err != nil {
				return err
			}
		case "mvhd":
			if s.mov.Timescale == 0 {
				s.mov.Timescale = s.parseTimescale(box)
			}
		case "mdhd":
			if tb != nil {
				tb.timescale = s.parseTimescale(box)
			}
		case "tkhd":
			if tb != nil {
				s.parseTKHD(box, tb)
			}
		case "hdlr":
			if tb != nil {
				s.parseHDLR(box, tb)
			}
		case "stsd":
			if tb != nil {
				s.parseSTSD(box, tb)
			}
		}
		off = next
	}
	return nil
}

func (s *scanner) parseTimescale(box BoxRef) uint32 {
	buf := make([]byte, 24)
	start := box.Offset + box.HeaderSize
	if err := s.readAt(start, buf); err != nil {
		return 0
	}
	if buf[0] == 1 {
		return binary.BigEndian.Uint32(buf[20:24]) // needs 24 bytes; caller's box is larger.
	}
	return binary.BigEndian.Uint32(buf[12:16])
}
