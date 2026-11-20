// Package bmff implements a bounded ISO BMFF (MP4) box parser.
// Box boundaries and hierarchy are parsed here; field-level decoding of
// known boxes lives in go_mp4_adapter.go and the index packages.
package bmff

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// MaxDepth bounds box nesting to keep malicious files from
	// exhausting the stack.
	MaxDepth = 16
	// MaxMetaBox caps how many bytes of a single metadata box payload
	// may be loaded into memory. mdat is never loaded through this path.
	MaxMetaBox = 16 << 20
	// MaxSample caps a single media sample read.
	MaxSample = 64 << 20
)

var (
	ErrTruncated = errors.New("bmff: truncated box")
	ErrTooDeep   = errors.New("bmff: box nesting too deep")
	ErrCorrupt   = errors.New("bmff: corrupt box structure")
)

// Box describes one box header.
type Box struct {
	Type    string
	Offset  int64 // absolute offset of the box header
	Size    int64 // total size including header
	HdrSize int64 // header size: 8, 16 (extended), plus 16 for uuid
	Depth   int
	Path    string // e.g. "moov/trak/mdia"
}

// DataOffset returns the absolute offset of the box payload.
func (b Box) DataOffset() int64 { return b.Offset + b.HdrSize }

// DataSize returns the payload size in bytes.
func (b Box) DataSize() int64 { return b.Size - b.HdrSize }

// End returns the offset one past the box.
func (b Box) End() int64 { return b.Offset + b.Size }

// Reader performs bounded reads over an io.ReaderAt.
type Reader struct {
	ra   io.ReaderAt
	size int64
}

// NewReader wraps ra which must hold exactly size bytes.
func NewReader(ra io.ReaderAt, size int64) *Reader {
	if size < 0 {
		size = 0
	}
	return &Reader{ra: ra, size: size}
}

// Size returns the total input size in bytes.
func (r *Reader) Size() int64 { return r.size }

// ReadAt reads len(p) bytes at off, guarding against out-of-range reads.
func (r *Reader) ReadAt(p []byte, off int64) error {
	if off < 0 || int64(len(p)) > r.size-off {
		return fmt.Errorf("%w: read of %d bytes at offset %d exceeds size %d",
			ErrTruncated, len(p), off, r.size)
	}
	if _, err := r.ra.ReadAt(p, off); err != nil {
		return fmt.Errorf("bmff: read at %d: %w", off, err)
	}
	return nil
}

// parseHeader decodes one box header starting at off. limit is the
// exclusive end of the enclosing region (file end or parent box end).
func (r *Reader) parseHeader(off, limit int64, depth int, path string) (Box, error) {
	if off < 0 || limit > r.size || off > limit {
		return Box{}, fmt.Errorf("%w: invalid region [%d,%d) of %d bytes",
			ErrCorrupt, off, limit, r.size)
	}
	var hdr [8]byte
	if err := r.ReadAt(hdr[:], off); err != nil {
		return Box{}, err
	}
	size := int64(binary.BigEndian.Uint32(hdr[0:4]))
	typ := string(hdr[4:8])
	hdrSize := int64(8)
	switch size {
	case 0:
		// Zero-size box extends to the end of the enclosing region.
		size = limit - off
	case 1:
		// 64-bit extended length.
		var ext [8]byte
		if err := r.ReadAt(ext[:], off+8); err != nil {
			return Box{}, err
		}
		u := binary.BigEndian.Uint64(ext[:])
		if u > uint64(1<<62) {
			return Box{}, fmt.Errorf("%w: box %q at %d declares overflowing size", ErrCorrupt, typ, off)
		}
		size = int64(u)
		hdrSize = 16
	}
	if typ == "uuid" {
		hdrSize += 16
	}
	if size < hdrSize {
		return Box{}, fmt.Errorf("%w: box %q at %d has size %d < header %d",
			ErrCorrupt, typ, off, size, hdrSize)
	}
	if size > limit-off {
		return Box{}, fmt.Errorf("%w: box %q at %d size %d exceeds region end %d",
			ErrTruncated, typ, off, size, limit)
	}
	return Box{Type: typ, Offset: off, Size: size, HdrSize: hdrSize, Depth: depth, Path: path + "/" + typ}, nil
}

// WalkFunc is invoked for every box. Returning descend=true walks into the
// box payload as a container. Unknown box types are skipped by default
// unless the callback chooses to descend.
type WalkFunc func(b Box) (descend bool, err error)

// Walk iterates sibling boxes in [off, limit). path names the parent.
func (r *Reader) Walk(off, limit int64, depth int, path string, fn WalkFunc) error {
	if depth > MaxDepth {
		return fmt.Errorf("%w: depth exceeds %d at %s", ErrTooDeep, MaxDepth, path)
	}
	pos := off
	for pos < limit {
		b, err := r.parseHeader(pos, limit, depth, path)
		if err != nil {
			return err
		}
		descend, err := fn(b)
		if err != nil {
			return err
		}
		if descend {
			if err := r.Walk(b.DataOffset(), b.End(), depth+1, b.Path, fn); err != nil {
				return err
			}
		}
		pos += b.Size // b.Size >= 8, so the loop always advances
	}
	return nil
}

// WalkTop iterates the top-level boxes of the file.
func (r *Reader) WalkTop(fn WalkFunc) error {
	return r.Walk(0, r.size, 0, "", fn)
}

// Children lists the immediate child boxes of a container.
func (r *Reader) Children(parent Box) ([]Box, error) {
	var out []Box
	err := r.Walk(parent.DataOffset(), parent.End(), parent.Depth+1, parent.Path,
		func(b Box) (bool, error) {
			out = append(out, b)
			return false, nil
		})
	return out, err
}

// ReadBoxData loads a metadata box payload, enforcing a byte cap.
func (r *Reader) ReadBoxData(b Box, max int64) ([]byte, error) {
	n := b.DataSize()
	if n > max {
		return nil, fmt.Errorf("%w: box %s payload %d bytes exceeds cap %d",
			ErrCorrupt, b.Path, n, max)
	}
	buf := make([]byte, n)
	if err := r.ReadAt(buf, b.DataOffset()); err != nil {
		return nil, err
	}
	return buf, nil
}

// ReadSample loads one media sample with a size cap.
func (r *Reader) ReadSample(off int64, size uint32) ([]byte, error) {
	if int64(size) > MaxSample {
		return nil, fmt.Errorf("%w: sample of %d bytes exceeds cap", ErrCorrupt, size)
	}
	buf := make([]byte, size)
	if err := r.ReadAt(buf, off); err != nil {
		return nil, err
	}
	return buf, nil
}
