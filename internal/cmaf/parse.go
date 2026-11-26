package cmaf

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"rtx/internal/model"
)

// Limits protect the parser from hostile sizes. A fixture fragment may not
// exceed 64 MiB; a box header claiming a larger size is rejected.
const (
	MaxFragmentBytes = 64 << 20
	MaxBoxDepth      = 8
	SmallHeaderSize  = 8
	LargeHeaderSize  = 16
)

// Code values specific to this package.
const (
	// CodeCMAFInvalid marks structurally invalid CMAF (bad box nesting, bad
	// version, missing required boxes).
	CodeCMAFInvalid model.Code = "cmaf_invalid"
	// CodeFragmentTooBig marks a fragment file or box that exceeds the limit.
	CodeFragmentTooBig model.Code = "fragment_too_large"
)

// Box is a parsed box with its absolute byte offset in the file.
type Box struct {
	Type       string
	Offset     int64
	HeaderSize int64
	Size       int64
	Payload    []byte
	Children   []*Box
}

// File is a parsed fragment file composed of top-level boxes.
type File struct {
	Path   string
	Size   int64
	Boxes  []*Box
	Digest [32]byte
}

// ReadFile parses a fragment or init file fully into memory, rejecting any
// length that escapes the file or exceeds the fragment limit. The first
// unreadable offset is reported with CodeTruncatedBox so the CLI can print it.
func ReadFile(path string) (*File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, model.E(model.CodeInvalidInput, "open fragment", "%v", err).WithPath(path).WithCause(err)
	}
	if !info.Mode().IsRegular() {
		return nil, model.E(model.CodeInvalidInput, "open fragment", "not a regular file").WithPath(path)
	}
	if info.Size() > MaxFragmentBytes {
		return nil, model.E(CodeFragmentTooBig, "read fragment",
			"%d bytes exceeds the %d byte fragment limit", info.Size(), MaxFragmentBytes).WithPath(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.E(model.CodeInvalidInput, "read fragment", "%v", err).WithPath(path).WithCause(err)
	}
	f, err := ParseBytes(path, data)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// ParseBytes parses the top-level box structure.
func ParseBytes(path string, data []byte) (*File, error) {
	f := &File{Path: path, Size: int64(len(data)), Digest: Digest(data)}
	if err := appendBoxes(f, data, 0, 0); err != nil {
		return nil, err
	}
	return f, nil
}

func appendBoxes(f *File, data []byte, base int64, depth int) error {
	return parseChildren(f.Path, data, base, depth, func(b *Box) {
		f.Boxes = append(f.Boxes, b)
	})
}

// parseChildren walks a sequence of sibling boxes and calls add for each.
func parseChildren(path string, data []byte, base int64, depth int, add func(*Box)) error {
	if depth > MaxBoxDepth {
		return model.E(CodeCMAFInvalid, "parse box", "box nesting deeper than %d", MaxBoxDepth).WithPath(path).WithByte(base)
	}
	for off := int64(0); off < int64(len(data)); {
		b, err := parseBox(path, data, off, base, depth)
		if err != nil {
			return err
		}
		add(b)
		off += b.Size
	}
	return nil
}

// parseBox parses a single box header plus payload at data[off:].
func parseBox(path string, data []byte, off int64, base int64, depth int) (*Box, error) {
	abs := base + off
	remaining := int64(len(data)) - off
	if remaining < SmallHeaderSize {
		return nil, model.E(model.CodeTruncatedBox, "parse box",
			"%d byte(s) remain, need at least %d for a box header", remaining, SmallHeaderSize).
			WithPath(path).WithByte(abs)
	}
	size := int64(binary.BigEndian.Uint32(data[off : off+4]))
	typ := string(data[off+4 : off+8])
	header := int64(SmallHeaderSize)
	if size == 1 {
		if remaining < LargeHeaderSize {
			return nil, model.E(model.CodeTruncatedBox, "parse box",
				"extended size box %q needs %d header bytes, %d remain", typ, LargeHeaderSize, remaining).
				WithPath(path).WithByte(abs)
		}
		size = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
		header = int64(LargeHeaderSize)
	} else if size == 0 {
		// size 0 means "to end of file" only for the last box; accept it and
		// let the caller's consistency checks handle anything else.
		size = remaining
	}
	if size < header {
		return nil, model.E(CodeCMAFInvalid, "parse box",
			"box %q declares size %d, smaller than its %d byte header", typ, size, header).
			WithPath(path).WithByte(abs)
	}
	if size > remaining {
		return nil, model.E(model.CodeTruncatedBox, "parse box",
			"box %q at %d declares %d bytes but only %d remain", typ, abs, size, remaining).
			WithPath(path).WithByte(abs)
	}
	b := &Box{
		Type:       typ,
		Offset:     abs,
		HeaderSize: header,
		Size:       size,
		Payload:    data[off+header : off+size],
	}
	if isContainer(typ) {
		if err := parseChildren(path, b.Payload, abs+header, depth+1, func(c *Box) {
			b.Children = append(b.Children, c)
		}); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// isContainer lists the boxes whose payload is a sequence of child boxes.
func isContainer(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "mvex", "moof", "traf", "edts":
		return true
	}
	return false
}

// Digest is the SHA-256 of the raw bytes, used for content addressing.
func Digest(data []byte) [32]byte {
	return sha256Sum(data)
}

// Find returns the first child of the given type, or nil.
func (b *Box) Find(typ string) *Box {
	for _, c := range b.Children {
		if c.Type == typ {
			return c
		}
	}
	return nil
}

// FindAll returns every child of the given type.
func (b *Box) FindAll(typ string) []*Box {
	var out []*Box
	for _, c := range b.Children {
		if c.Type == typ {
			out = append(out, c)
		}
	}
	return out
}

// Top returns the first top-level box of the given type.
func (f *File) Top(typ string) *Box {
	for _, b := range f.Boxes {
		if b.Type == typ {
			return b
		}
	}
	return nil
}

// Walk visits every box depth-first.
func (f *File) Walk(fn func(b *Box) error) error {
	var walk func(b *Box) error
	walk = func(b *Box) error {
		if err := fn(b); err != nil {
			return err
		}
		for _, c := range b.Children {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	for _, b := range f.Boxes {
		if err := walk(b); err != nil {
			return err
		}
	}
	return nil
}

// CopyTo writes the original file bytes; retained so artifact writers can
// normalize payloads without re-reading the source.
func (f *File) CopyTo(w io.Writer, data []byte) error {
	_, err := w.Write(data)
	return err
}

func describe(b *Box) string {
	return fmt.Sprintf("%s@%d+%d", b.Type, b.Offset, b.Size)
}
