package container

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"mediacore/internal/core"
)

// Limits bounds every read the scanner performs. They exist so a hostile or
// damaged file cannot make the process allocate without bound.
type Limits struct {
	MaxFileBytes     int64
	MaxBoxes         int
	MaxDepth         int
	MaxTracks        int
	MaxSamples       int
	MaxTableBytes    int64
	MaxPayloadSample int64
}

// DefaultLimits are deliberately generous for audio-only material but small
// enough that malformed input fails fast.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:  1 << 30,
		MaxBoxes:      8192,
		MaxDepth:      16,
		MaxTracks:     32,
		MaxSamples:    1 << 22,
		MaxTableBytes: 64 << 20,
	}
}

// Box is one node of the parsed box tree. Sizes are clamped to the bytes that
// actually exist, so a truncated box is described rather than rejected.
type Box struct {
	Type       string
	Offset     int64
	Size       int64
	HeaderSize int64
	Depth      int
	Truncated  bool
	Note       string
	Children   []*Box
}

// PayloadOffset is the first byte after the box header.
func (b *Box) PayloadOffset() int64 { return b.Offset + b.HeaderSize }

// PayloadSize is the number of payload bytes present in the file.
func (b *Box) PayloadSize() int64 { return b.Size - b.HeaderSize }

// Find returns the first descendant with the given type.
func (b *Box) Find(typ string) *Box {
	for _, c := range b.Children {
		if c.Type == typ {
			return c
		}
		if got := c.Find(typ); got != nil {
			return got
		}
	}
	return nil
}

// Walk visits this box and every descendant in file order.
func (b *Box) Walk(fn func(*Box) bool) {
	if !fn(b) {
		return
	}
	for _, c := range b.Children {
		c.Walk(fn)
	}
}

type scanState struct {
	file  *os.File
	size  int64
	lim   Limits
	count int
}

// children walks sibling boxes in [start,end). On the first structurally
// broken header it records a truncated box and stops that level.
func (st *scanState) children(start, end int64, depth int) ([]*Box, *core.Error) {
	var out []*Box
	off := start
	for off < end {
		if st.lim.MaxBoxes > 0 && st.count >= st.lim.MaxBoxes {
			return out, core.New(core.CodeLimitExceeded, core.StageScan, false,
				fmt.Sprintf("box count exceeds %d", st.lim.MaxBoxes)).WithOffset(off)
		}
		if depth > st.lim.MaxDepth {
			return out, core.New(core.CodeLimitExceeded, core.StageScan, false,
				fmt.Sprintf("box nesting exceeds depth %d", st.lim.MaxDepth)).WithOffset(off)
		}
		remain := end - off
		if remain < 8 {
			// Trailing padding shorter than a header is not a box; only
			// report it when it is inside a container we were told to read.
			return out, core.New(core.CodeBoxTruncated, core.StageScan, true,
				fmt.Sprintf("%d trailing bytes cannot form a box header", remain)).WithOffset(off)
		}
		var hdr [16]byte
		if _, err := st.file.ReadAt(hdr[:8], off); err != nil {
			return out, core.Wrap(core.CodeIO, core.StageScan, false, "read box header", err).WithOffset(off)
		}
		sz := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		header := int64(8)
		if sz == 1 {
			if remain < 16 {
				return out, core.New(core.CodeBoxTruncated, core.StageScan, true,
					"64-bit box header truncated").WithOffset(off)
			}
			if _, err := st.file.ReadAt(hdr[8:16], off+8); err != nil {
				return out, core.Wrap(core.CodeIO, core.StageScan, false, "read extended box header", err).WithOffset(off)
			}
			large := binary.BigEndian.Uint64(hdr[8:16])
			if large > uint64(end-off) {
				return st.truncatedTail(out, off, typ, header, end)
			}
			sz = int64(large)
			header = 16
		} else if sz == 0 {
			sz = end - off
		}
		st.count++
		if sz < header {
			return out, core.New(core.CodeBoxTruncated, core.StageScan, false,
				fmt.Sprintf("box %q size %d smaller than header %d", typ, sz, header)).WithOffset(off)
		}
		b := &Box{Type: typ, Offset: off, Size: sz, HeaderSize: header, Depth: depth}
		if !isPrintableType(typ) {
			return out, core.New(core.CodeBoxTruncated, core.StageScan, false,
				fmt.Sprintf("implausible box type %q", typ)).WithOffset(off)
		}
		if off+sz > end {
			b.Truncated = true
			b.Size = end - off
			b.Note = fmt.Sprintf("declared size %d runs past readable end %d", sz, end)
			out = append(out, b)
			return out, core.New(core.CodeBoxTruncated, core.StageScan, true,
				fmt.Sprintf("box %q truncated: %s", typ, b.Note)).WithOffset(off)
		}
		if isContainer(typ) && typ != "mdat" {
			extra := containerExtra[typ]
			childStart := b.PayloadOffset() + extra
			if childStart > off+sz {
				b.Truncated = true
			} else {
				kids, cerr := st.children(childStart, off+sz, depth+1)
				b.Children = kids
				if cerr != nil {
					b.Note = cerr.Detail
					if cerr.Code == core.CodeBoxTruncated && cerr.Recoverable {
						b.Truncated = true
					}
					out = append(out, b)
					return out, cerr
				}
			}
		}
		out = append(out, b)
		off += sz
	}
	return out, nil
}

// truncatedTail records the final box when the file ends mid-box. A box whose
// header claims more than the remaining bytes is still useful: its readable
// prefix is reported and recovery decides how much of it to trust.
func (st *scanState) truncatedTail(out []*Box, off int64, typ string, header, end int64) ([]*Box, *core.Error) {
	st.count++
	b := &Box{Type: typ, Offset: off, Size: end - off, HeaderSize: header, Truncated: true,
		Note: "declared size exceeds file length"}
	out = append(out, b)
	return out, core.New(core.CodeBoxTruncated, core.StageScan, true,
		fmt.Sprintf("box %q declares more bytes than the file holds", typ)).WithOffset(off)
}

func isPrintableType(typ string) bool {
	for i := 0; i < len(typ); i++ {
		c := typ[i]
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func readFTyp(f io.ReaderAt, b *Box) string {
	if b.PayloadSize() < 8 {
		return ""
	}
	var buf [4]byte
	if _, err := f.ReadAt(buf[:], b.PayloadOffset()); err != nil {
		return ""
	}
	return string(buf[:])
}
