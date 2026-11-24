// Package scan streams MP4/MOV box structure from disk. Payload bytes are
// never buffered wholesale: only box headers and the sample tables inside
// moov are read, so long media files are handled in constant memory relative
// to payload size.
package scan

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"archivist/internal/fault"
)

// BoxRef locates one box in the file.
type BoxRef struct {
	Type       string `json:"type"`
	Offset     int64  `json:"offset"`
	Size       uint64 `json:"size"`
	HeaderSize uint64 `json:"header_size"`
	Depth      int    `json:"depth"`
}

// End returns the exclusive end offset of the box.
func (b BoxRef) End() int64 { return b.Offset + int64(b.Size) }

// Container is the scan result: validated box layout plus decoded sample
// tables for every track.
type Container struct {
	File           string   `json:"file"`
	Size           int64    `json:"size"`
	MovieTimescale uint32   `json:"movie_timescale"`
	Boxes          []BoxRef `json:"boxes"`
	Skipped        []BoxRef `json:"skipped"`
	Warnings       []string `json:"warnings,omitempty"`
	Tracks         []*Track `json:"tracks"`
}

// Track is one media track with its sample table.
type Track struct {
	ID        uint32 `json:"id"`
	Timescale uint32 `json:"timescale"`
	Handler   string `json:"handler"`
	Duration  uint64 `json:"duration"`
	Width     uint32 `json:"width"`
	Height    uint32 `json:"height"`
	// STSD locates the sample description box. Its payload is copied
	// verbatim when the file is rebuilt, so codec configuration survives
	// without being interpreted.
	STSD    BoxRef   `json:"stsd"`
	Samples []Sample `json:"samples"`
}

// Sample is one decoded sample entry.
type Sample struct {
	Index    int    `json:"index"`
	DTS      int64  `json:"dts"`
	PTS      int64  `json:"pts"` // 32-bit presentation timestamp as stored
	Size     uint32 `json:"size"`
	Offset   int64  `json:"offset"`
	Keyframe bool   `json:"keyframe"`
}

// Reader scans a single file.
type Reader struct {
	path string
	f    *os.File
	size int64
}

// Open prepares path for scanning.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.New(fault.KindIO, fault.StageScan, "open input").In(path).Wrap(err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fault.New(fault.KindIO, fault.StageScan, "stat input").In(path).Wrap(err)
	}
	if st.IsDir() {
		f.Close()
		return nil, fault.New(fault.KindIO, fault.StageScan, "input is a directory").In(path)
	}
	return &Reader{path: path, f: f, size: st.Size()}, nil
}

// Close releases the file handle.
func (r *Reader) Close() error { return r.f.Close() }

// containerType reports whether typ has children we must walk.
func containerType(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "edts", "mvex":
		return true
	}
	return false
}

// ignorableType reports boxes the scanner skips without descending.
func ignorableType(typ string) bool {
	switch typ {
	case "free", "skip", "wide", "uuid", "mfra", "udta", "meta", "ilst":
		return true
	}
	return false
}

// Scan walks the container and decodes the sample tables.
func (r *Reader) Scan() (*Container, error) {
	c := &Container{File: r.path, Size: r.size}
	if err := r.walk(0, r.size, 0, c, true); err != nil {
		return nil, err
	}
	if err := r.decodeTables(c); err != nil {
		return nil, err
	}
	if c.MovieTimescale == 0 {
		return nil, fault.New(fault.KindCorrupt, fault.StageScan,
			"no readable mvhd; movie timescale is undefined").In(r.path).At(0)
	}
	return c, nil
}

// walk validates boxes in [start,end) and appends refs to c.
func (r *Reader) walk(start, end int64, depth int, c *Container, root bool) error {
	if depth > 16 {
		return fault.New(fault.KindCorrupt, fault.StageScan,
			"box nesting deeper than 16").In(r.path).At(start)
	}
	pos := start
	for pos < end {
		remaining := end - pos
		if remaining < 8 {
			if root {
				return fault.Corrupt(fault.StageScan, r.path, pos, "",
					"truncated box header: %d bytes available, need 8", remaining)
			}
			return fault.Corrupt(fault.StageScan, r.path, pos, "",
				"truncated box header inside parent: %d bytes available, need 8", remaining)
		}
		var hdr [8]byte
		if _, err := r.f.ReadAt(hdr[:], pos); err != nil {
			return fault.Corrupt(fault.StageScan, r.path, pos, "", "read box header").Wrap(err)
		}
		sizeField := binary.BigEndian.Uint32(hdr[:4])
		typ := string(hdr[4:])
		headerSize := uint64(8)
		var size uint64
		switch sizeField {
		case 0:
			size = uint64(end - pos)
		case 1:
			if remaining < 16 {
				return fault.Corrupt(fault.StageScan, r.path, pos, typ,
					"truncated large box header: %d bytes available, need 16", remaining)
			}
			var ext [8]byte
			if _, err := r.f.ReadAt(ext[:], pos+8); err != nil {
				return fault.Corrupt(fault.StageScan, r.path, pos, typ, "read large box header").Wrap(err)
			}
			size = binary.BigEndian.Uint64(ext[:])
			headerSize = 16
		default:
			size = uint64(sizeField)
		}
		if typ == "\x00\x00\x00\x00" {
			return fault.Corrupt(fault.StageScan, r.path, pos, "", "box type is all zero bytes")
		}
		if size < headerSize {
			return fault.Corrupt(fault.StageScan, r.path, pos, typ,
				"box length %d is smaller than its %d-byte header", size, headerSize)
		}
		if int64(size) > remaining {
			return fault.Corrupt(fault.StageScan, r.path, pos, typ,
				"box length %d overruns container end %d by %d bytes", size, end, int64(size)-remaining)
		}
		ref := BoxRef{Type: typ, Offset: pos, Size: size, HeaderSize: headerSize, Depth: depth}
		c.Boxes = append(c.Boxes, ref)
		switch {
		case containerType(typ):
			if err := r.walk(pos+int64(headerSize), pos+int64(size), depth+1, c, false); err != nil {
				return err
			}
		case ignorableType(typ):
			c.Skipped = append(c.Skipped, ref)
		default:
			// Unknown but well-formed boxes are legal: skip their payload.
			// Known leaf boxes are decoded later from their refs.
			if !knownLeaf(typ) {
				c.Skipped = append(c.Skipped, ref)
				c.Warnings = append(c.Warnings, fmt.Sprintf("skipped unknown box %q at %d", typ, pos))
			}
		}
		pos += int64(size)
	}
	if pos != end {
		return fault.Corrupt(fault.StageScan, r.path, pos, "",
			"box stream ended at %d, parent ends at %d", pos, end)
	}
	return nil
}

func knownLeaf(typ string) bool {
	switch typ {
	case "ftyp", "mdat", "mvhd", "tkhd", "mdhd", "hdlr", "stsd", "stts",
		"ctts", "stss", "stsz", "stsc", "stco", "co64", "vmhd", "smhd", "dref", "url ":
		return true
	}
	return false
}

// findFirst returns the first box of typ at depth <= maxDepth.
func (c *Container) findFirst(typ string, maxDepth int) (BoxRef, bool) {
	for _, b := range c.Boxes {
		if b.Type == typ && b.Depth <= maxDepth {
			return b, true
		}
	}
	return BoxRef{}, false
}

func (c *Container) findAll(typ string) []BoxRef {
	var out []BoxRef
	for _, b := range c.Boxes {
		if b.Type == typ {
			out = append(out, b)
		}
	}
	return out
}

// payloadReader returns a ReadSeeker over the box payload only.
func (r *Reader) payloadReader(ref BoxRef) io.ReadSeeker {
	return io.NewSectionReader(r.f, ref.Offset+int64(ref.HeaderSize), int64(ref.Size-ref.HeaderSize))
}

// unmarshal decodes ref's payload into dst using go-mp4.
func (r *Reader) unmarshal(ref BoxRef, dst mp4.IBox) error {
	payloadLen := ref.Size - ref.HeaderSize
	n, err := mp4.Unmarshal(r.payloadReader(ref), payloadLen, dst, mp4.Context{})
	if err != nil {
		return fault.Corrupt(fault.StageScan, r.path, ref.Offset, ref.Type,
			"cannot decode %s payload", ref.Type).Wrap(err)
	}
	if n != payloadLen {
		return fault.Corrupt(fault.StageScan, r.path, ref.Offset, ref.Type,
			"box declares %d payload bytes but only %d decode", payloadLen, n)
	}
	return nil
}

// errNoFile is returned when a Reader is used after Close.
var errNoFile = errors.New("scan: reader closed")
