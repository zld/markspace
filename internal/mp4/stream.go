// Package mp4 implements a bounded sequential MP4 scanner plus index
// construction and recovery. Every read is checked against the declared
// input size so a malformed box length can never make us read past the end of
// the data we were given.
package mp4

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"mediapipe/internal/model"
)

// Structural box errors. They are always wrapped in a *model.Error that
// records the byte offset of the offending box.
var (
	ErrTruncated     = errors.New("mp4: truncated box")
	ErrInvalidLength = errors.New("mp4: invalid box length")
	ErrOverflow      = errors.New("mp4: box length overflow")
	ErrOutOfBounds   = errors.New("mp4: box reference out of bounds")
	ErrTooManyBoxes  = errors.New("mp4: box count exceeds configured limit")
	ErrDepth         = errors.New("mp4: nesting depth exceeds configured limit")
	ErrTableTooLarge = errors.New("mp4: sample table exceeds memory limit")
)

// Config bounds the scanner so hostile input cannot exhaust memory.
type Config struct {
	MaxBoxes      int
	MaxDepth      int
	MaxTableBytes int64
	MaxSamples    int
}

// DefaultConfig returns conservative limits for local media regression runs.
func DefaultConfig() Config {
	return Config{
		MaxBoxes:      100_000,
		MaxDepth:      12,
		MaxTableBytes: 32 << 20,
		MaxSamples:    1 << 20,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.MaxBoxes <= 0 {
		c.MaxBoxes = d.MaxBoxes
	}
	if c.MaxDepth <= 0 {
		c.MaxDepth = d.MaxDepth
	}
	if c.MaxTableBytes <= 0 {
		c.MaxTableBytes = d.MaxTableBytes
	}
	if c.MaxSamples <= 0 {
		c.MaxSamples = d.MaxSamples
	}
	return c
}

// Box is one parsed box header with its absolute file offset. Path lists all
// enclosing boxes from the root, so callers can identify e.g. a stbl table
// without rescanning.
type Box struct {
	Offset     int64
	Size       int64
	HeaderSize int64
	Type       string
	Path       []string
	ExtendsEOF bool
}

// PayloadEnd returns the exclusive end offset of the box.
func (b Box) PayloadEnd() int64 { return b.Offset + b.Size }

// Payload returns the box payload as a bounded slice.
func (b Box) PayloadSize() int64 { return b.Size - b.HeaderSize }

// Stream scans a fixed-size byte source. It never reads outside [0,size).
type Stream struct {
	r     io.ReaderAt
	size  int64
	asset string
	cfg   Config
	boxes int
}

// NewStream validates the source bounds and configuration.
func NewStream(r io.ReaderAt, size int64, asset string, cfg Config) (*Stream, error) {
	if r == nil {
		return nil, model.New(model.KindInternal, model.StageMP4Scan, asset, "new", errors.New("nil reader"))
	}
	if size < 0 {
		return nil, model.New(model.KindInvalid, model.StageMP4Scan, asset, "new",
			fmt.Errorf("negative input size %d", size))
	}
	return &Stream{r: r, size: size, asset: asset, cfg: cfg.withDefaults()}, nil
}

// Size returns the declared input size.
func (s *Stream) Size() int64 { return s.size }

func (s *Stream) corrupt(op string, off int64, cause error) error {
	return model.New(model.KindCorrupt, model.StageMP4Scan, s.asset, op, cause).WithOffset(off)
}

// readAt reads exactly len(p) bytes at off without crossing the input end.
func (s *Stream) readAt(p []byte, off int64) error {
	if off < 0 || off > s.size {
		return s.corrupt("read", off, fmt.Errorf("%w: offset %d outside [0,%d)", ErrOutOfBounds, off, s.size))
	}
	if int64(len(p)) > s.size-off {
		return s.corrupt("read", off, fmt.Errorf("%w: need %d bytes at %d, only %d available",
			ErrTruncated, len(p), off, s.size-off))
	}
	n, err := s.r.ReadAt(p, off)
	if n < len(p) {
		if err == nil || errors.Is(err, io.EOF) {
			err = fmt.Errorf("%w: short read %d/%d", ErrTruncated, n, len(p))
		}
		return s.corrupt("read", off, err)
	}
	if err != nil {
		return s.corrupt("read", off, err)
	}
	return nil
}

// headerAt parses the common box header at off, validating the size against
// both the input size and the enclosing container's end.
func (s *Stream) headerAt(off, limit int64) (Box, error) {
	var small [8]byte
	if err := s.readAt(small[:], off); err != nil {
		return Box{}, err
	}
	rawSize := binary.BigEndian.Uint32(small[:4])
	b := Box{Offset: off, Type: string(small[4:8]), HeaderSize: 8}
	size := int64(rawSize)
	switch rawSize {
	case 0:
		// "extends to end of file/container" is legal; clamp to the limit.
		b.ExtendsEOF = true
		size = limit - off
	case 1:
		var large [8]byte
		if err := s.readAt(large[:], off+8); err != nil {
			return Box{}, err
		}
		v := binary.BigEndian.Uint64(large[:])
		if v > math.MaxInt64 {
			return Box{}, s.corrupt("read_header", off, fmt.Errorf("%w: 64-bit size %d", ErrOverflow, v))
		}
		b.HeaderSize = 16
		size = int64(v)
	}
	if size < b.HeaderSize {
		return Box{}, s.corrupt("read_header", off, fmt.Errorf(
			"%w: box %q size %d smaller than header %d", ErrInvalidLength, b.Type, size, b.HeaderSize))
	}
	if size > limit-off {
		return Box{}, s.corrupt("read_header", off, fmt.Errorf(
			"%w: box %q size %d exceeds container end %d", ErrOutOfBounds, b.Type, size, limit))
	}
	if size > s.size-off {
		return Box{}, s.corrupt("read_header", off, fmt.Errorf(
			"%w: box %q ends at %d beyond input size %d", ErrTruncated, b.Type, off+size, s.size))
	}
	b.Size = size
	return b, nil
}

// Walk visits every box depth-first, parents before children, and records the
// absolute offset of each. A callback error aborts the walk unchanged.
func (s *Stream) Walk(ctx context.Context, fn func(Box) error) error {
	return s.walk(ctx, 0, s.size, nil, 0, fn)
}

func (s *Stream) walk(ctx context.Context, start, end int64, path []string, depth int, fn func(Box) error) error {
	off := start
	for off < end {
		if err := ctx.Err(); err != nil {
			return model.New(model.KindCancelled, model.StageMP4Scan, s.asset, "walk", err).WithOffset(off)
		}
		b, err := s.headerAt(off, end)
		if err != nil {
			return err
		}
		s.boxes++
		if s.boxes > s.cfg.MaxBoxes {
			return s.corrupt("walk", off, fmt.Errorf("%w (%d)", ErrTooManyBoxes, s.cfg.MaxBoxes))
		}
		b.Path = append(append([]string(nil), path...), b.Type)
		if err := fn(b); err != nil {
			return err
		}
		if isContainer(b.Type) {
			if depth+1 > s.cfg.MaxDepth {
				return s.corrupt("walk", off, fmt.Errorf("%w (%d) at %q", ErrDepth, s.cfg.MaxDepth, b.Type))
			}
			if err := s.walk(ctx, off+b.HeaderSize, off+b.Size, b.Path, depth+1, fn); err != nil {
				return err
			}
		}
		off += b.Size
	}
	if off != end {
		return s.corrupt("walk", off, fmt.Errorf("%w: container ends at %d, expected %d", ErrTruncated, off, end))
	}
	return nil
}

// Payload copies a box payload into memory, refusing tables larger than the
// configured budget.
func (s *Stream) Payload(b Box) ([]byte, error) {
	n := b.PayloadSize()
	if n < 0 {
		return nil, s.corrupt("payload", b.Offset, fmt.Errorf("%w: negative payload", ErrInvalidLength))
	}
	if n > s.cfg.MaxTableBytes {
		return nil, s.corrupt("payload", b.Offset, fmt.Errorf(
			"%w: %q payload %d > %d", ErrTableTooLarge, b.Type, n, s.cfg.MaxTableBytes))
	}
	buf := make([]byte, n)
	if err := s.readAt(buf, b.Offset+b.HeaderSize); err != nil {
		return nil, err
	}
	return buf, nil
}

// Config exposes the effective scanner limits to the index builder.
func (s *Stream) Config() Config { return s.cfg }

func isContainer(t string) bool {
	switch t {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "mvex", "moof", "traf", "dinf", "udta":
		return true
	}
	return false
}
