// Package ebml implements a bounded, streaming reader for the subset of EBML
// used by Matroska and WebM. This file declares the walker's public types.
package ebml

import (
	"bufio"
	"fmt"
	"io"
)

// UnknownSize is the size value of an element that omits its length.
const UnknownSize uint64 = ^uint64(0)

// DefaultMaxElementSize is the 8 MiB cap on a single buffered payload.
const DefaultMaxElementSize int64 = 8 << 20

// maxDepth guards against pathological nesting.
const maxDepth = 32

// Kind is the walker event kind.
type Kind uint8

const (
	// Start begins a master element.
	Start Kind = iota
	// End closes a master element.
	End
	// Leaf is a complete leaf payload.
	Leaf
)

// Event is one structural event from the walker.
type Event struct {
	Kind Kind
	ID   ID
	// Size is the declared payload size; UnknownSize for unknown-size
	// masters.
	Size uint64
	// HeaderOffset is the absolute offset of the element id.
	HeaderOffset int64
	// DataOffset is the absolute offset of the payload.
	DataOffset int64
	// Data holds the leaf payload (never more than MaxElementSize bytes).
	Data []byte
	// Unknown reports that the master omitted its size.
	Unknown bool
	// Depth is the nesting depth; top-level elements are depth 0.
	Depth int
}

// String renders a short event description.
func (e Event) String() string {
	return fmt.Sprintf("%s id=0x%X size=%d @%d", [...]string{"start", "end", "leaf"}[e.Kind], uint32(e.ID), e.Size, e.HeaderOffset)
}

// ReaderOptions configures a Walker.
type ReaderOptions struct {
	// MaxElementSize caps a single buffered payload. Default 8 MiB.
	MaxElementSize int64
}

type scope struct {
	id      ID
	end     int64 // absolute end offset, valid when !unknown
	unknown bool
	start   int64
}

// Walker streams EBML events.
type Walker struct {
	br   *bufio.Reader
	off  int64
	max  int64
	opts ReaderOptions

	stack []scope
	done  bool
}

// NewWalker returns a walker over r.
func NewWalker(r io.Reader, opts ReaderOptions) *Walker {
	if opts.MaxElementSize <= 0 {
		opts.MaxElementSize = DefaultMaxElementSize
	}
	return &Walker{
		br:   bufio.NewReaderSize(r, 64<<10),
		max:  opts.MaxElementSize,
		opts: opts,
	}
}

// Offset returns the absolute offset of the next unread byte.
func (w *Walker) Offset() int64 { return w.off }
