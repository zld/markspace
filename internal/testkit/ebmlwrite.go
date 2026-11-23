// Package testkit builds deterministic Matroska/WebM, MP4 and RTP fixtures in
// memory. Nothing here reads external files or touches the network.
package testkit

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"mediad/internal/ebml"
)

// Element is one node of an EBML tree. A node is a master when it has
// Children; otherwise Data is its payload.
type Element struct {
	ID       ebml.ID
	Data     []byte
	Children []*Element
	// UnknownSize writes the reserved unknown-size marker instead of a
	// computed payload size. Only valid for Segment, Cluster and BlockGroup.
	UnknownSize bool

	// Offset, HeaderSize and Size are filled by Serialize.
	Offset     int64
	HeaderSize int
	Size       int64
}

// Master builds a master element.
func Master(id ebml.ID, children ...*Element) *Element {
	return &Element{ID: id, Children: children}
}

// Bytes builds a binary leaf.
func Bytes(id ebml.ID, data []byte) *Element {
	return &Element{ID: id, Data: append([]byte(nil), data...)}
}

// Uint builds an unsigned integer leaf using the minimal byte width.
func Uint(id ebml.ID, v uint64) *Element {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	n := 1
	for i := 0; i < 8; i++ {
		if buf[i] != 0 {
			n = 8 - i
			break
		}
	}
	return &Element{ID: id, Data: append([]byte(nil), buf[8-n:]...)}
}

// Sint builds a signed integer leaf using the minimal byte width.
func Sint(id ebml.ID, v int64) *Element {
	n := 1
	for _, w := range []int{2, 3, 4, 5, 6, 7, 8} {
		lo := -(int64(1) << (w*8 - 1))
		hi := int64(1)<<(w*8-1) - 1
		if v >= lo && v <= hi {
			break
		}
		n = w
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	return &Element{ID: id, Data: append([]byte(nil), buf[8-n:]...)}
}

// Float builds a 64-bit float leaf.
func Float(id ebml.ID, f float64) *Element {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], math.Float64bits(f))
	return &Element{ID: id, Data: buf[:]}
}

// Str builds a NUL-padded string leaf.
func Str(id ebml.ID, s string) *Element {
	return &Element{ID: id, Data: append([]byte(s), 0)}
}

// IDBytes encodes an EBML id as stored on disk.
func IDBytes(id ebml.ID) []byte {
	v := uint32(id)
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	n := 1
	switch {
	case v >= 1<<24:
		n = 4
	case v >= 1<<16:
		n = 3
	case v >= 1<<8:
		n = 2
	}
	return append([]byte(nil), buf[4-n:]...)
}

// SizeBytes encodes an EBML size VINT in its shortest form, reserving the
// all-ones marker for the unknown-size case.
func SizeBytes(v uint64) []byte {
	for n := 1; n <= 8; n++ {
		if n < 8 && v >= (uint64(1)<<(7*n))-1 {
			continue
		}
		buf := make([]byte, n)
		x := v | uint64(1)<<(7*n)
		for i := n - 1; i >= 0; i-- {
			buf[i] = byte(x)
			x >>= 8
		}
		return buf
	}
	panic("size too large")
}

// UnknownSizeBytes is the reserved 8-byte unknown-size marker.
var UnknownSizeBytes = []byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

// Serialize writes the tree into one byte slice and annotates every element
// with its absolute offset and encoded size.
//
// Sizes are computed bottom-up before any bytes are written, so a master's
// size VINT always has the width its final payload requires.
func Serialize(root *Element) ([]byte, error) {
	seen := map[*Element]bool{}
	if err := annotate(root, seen); err != nil {
		return nil, err
	}
	out := make([]byte, 0, root.Size)
	if err := emit(root, &out); err != nil {
		return nil, err
	}
	if int64(len(out)) != root.Size {
		return nil, fmt.Errorf("testkit: serialized %d bytes, planned %d", len(out), root.Size)
	}
	return out, nil
}

// annotate computes each node's payload and header widths.
func annotate(e *Element, seen map[*Element]bool) error {
	if seen[e] {
		return fmt.Errorf("testkit: element reused in tree")
	}
	seen[e] = true
	idLen := len(IDBytes(e.ID))
	if !ebml.IsMaster(e.ID) {
		e.HeaderSize = idLen + len(SizeBytes(uint64(len(e.Data))))
		e.Size = int64(e.HeaderSize + len(e.Data))
		return nil
	}
	var payload uint64
	for _, c := range e.Children {
		if err := annotate(c, seen); err != nil {
			return err
		}
		payload += uint64(c.Size)
	}
	if e.UnknownSize {
		e.HeaderSize = idLen + len(UnknownSizeBytes)
	} else {
		e.HeaderSize = idLen + len(SizeBytes(payload))
	}
	e.Size = int64(e.HeaderSize) + int64(payload)
	return nil
}

// emit writes a pre-annotated node.
func emit(e *Element, out *[]byte) error {
	e.Offset = int64(len(*out))
	*out = append(*out, IDBytes(e.ID)...)
	if !ebml.IsMaster(e.ID) {
		*out = append(*out, SizeBytes(uint64(len(e.Data)))...)
		*out = append(*out, e.Data...)
		return nil
	}
	if e.UnknownSize {
		*out = append(*out, UnknownSizeBytes...)
	} else {
		var payload uint64
		for _, c := range e.Children {
			payload += uint64(c.Size)
		}
		*out = append(*out, SizeBytes(payload)...)
	}
	for _, c := range e.Children {
		if err := emit(c, out); err != nil {
			return err
		}
	}
	return nil
}

// OffsetsOf returns absolute offsets for elements selected by id, in document
// order. It is a convenience for tests.
func OffsetsOf(root *Element, id ebml.ID) []int64 {
	var res []int64
	var walk func(e *Element)
	walk = func(e *Element) {
		if e.ID == id {
			res = append(res, e.Offset)
		}
		for _, c := range e.Children {
			walk(c)
		}
	}
	walk(root)
	sort.Slice(res, func(i, j int) bool { return res[i] < res[j] })
	return res
}
