package media

import (
	"bytes"
	"fmt"
	"io"
)

type boxSpan struct {
	offset    uint64
	size      uint64
	headerLen uint64
	typ       [4]byte
}

func (b boxSpan) payloadStart() uint64 { return b.offset + b.headerLen }
func (b boxSpan) payloadSize() uint64  { return b.size - b.headerLen }
func (b boxSpan) end() uint64          { return b.offset + b.size }

// topLevelBoxes walks the root box sequence. It rejects a truncated or
// overrunning box rather than trusting later offsets.
func topLevelBoxes(r *bytes.Reader) ([]boxSpan, error) {
	var out []boxSpan
	total := uint64(r.Len())
	var off uint64
	for off < total {
		b, err := readBoxHeader(r, off, total)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
		off = b.end()
	}
	return out, nil
}

func readBoxHeader(r *bytes.Reader, off, total uint64) (boxSpan, error) {
	if total-off < 8 {
		return boxSpan{}, fmt.Errorf("truncated box header at offset %d (%d bytes left)", off, total-off)
	}
	if _, err := r.Seek(int64(off), io.SeekStart); err != nil {
		return boxSpan{}, err
	}
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return boxSpan{}, err
	}
	size := uint64(beU32(hdr[:4]))
	b := boxSpan{offset: off, size: size, headerLen: 8}
	copy(b.typ[:], hdr[4:8])
	switch size {
	case 0:
		b.size = total - off
	case 1:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return boxSpan{}, fmt.Errorf("truncated large-size header for %q at offset %d", string(b.typ[:]), off)
		}
		b.size = beU64(ext[:])
		b.headerLen = 16
	}
	if b.size < b.headerLen {
		return boxSpan{}, fmt.Errorf("box %q at offset %d has size %d smaller than its header", string(b.typ[:]), off, b.size)
	}
	if b.offset+b.size > total {
		return boxSpan{}, fmt.Errorf("box %q at offset %d declares size %d beyond file end %d", string(b.typ[:]), off, b.size, total)
	}
	return b, nil
}

// childBoxes lists direct children of parent with the given type.
func childBoxes(r *bytes.Reader, parent boxSpan, typ string) ([]boxSpan, error) {
	total := parent.end()
	var out []boxSpan
	off := parent.payloadStart()
	for off < total {
		b, err := readBoxHeader(r, off, total)
		if err != nil {
			return nil, err
		}
		if string(b.typ[:]) == typ {
			out = append(out, b)
		}
		off = b.end()
	}
	return out, nil
}

func findBox(boxes []boxSpan, typ string) (boxSpan, bool) {
	for _, b := range boxes {
		if string(b.typ[:]) == typ {
			return b, true
		}
	}
	return boxSpan{}, false
}

// parseTrack builds a Track from one trak box.
func beU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func beU64(b []byte) uint64 {
	return uint64(beU32(b))<<32 | uint64(beU32(b[4:]))
}
