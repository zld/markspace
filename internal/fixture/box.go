package fixture

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
)

// box is a functional fMP4 builder. Every box is assembled in memory, so a
// parent can compute a child's size before writing its header, which is what
// the trun data-offset needs. Building bottom-up avoids the back-patching that
// mp4.Writer requires and keeps the generators readable.
type box struct {
	typ  mp4.BoxType
	data []byte
}

// fullBox is a box with a version/flags header.
type fullBox struct {
	box
	version uint8
	flags   uint32
}

func (b box) bytes() []byte {
	out := make([]byte, 8+len(b.data))
	out[0] = byte((uint64(len(out)) >> 24) & 0xff)
	out[1] = byte((uint64(len(out)) >> 16) & 0xff)
	out[2] = byte((uint64(len(out)) >> 8) & 0xff)
	out[3] = byte(uint64(len(out)) & 0xff)
	copy(out[4:8], b.typ[:])
	copy(out[8:], b.data)
	return out
}

// concat joins child boxes into one payload.
func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// leaf marshals a go-mp4 box and returns it as a plain box.
func leaf(v mp4.IImmutableBox) box {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, v, mp4.Context{}); err != nil {
		panic(fmt.Sprintf("marshal %s: %v", v.GetType().String(), err))
	}
	return box{typ: v.GetType(), data: buf.Bytes()}
}

// container builds a parent from its children.
func container(t mp4.BoxType, children ...box) box {
	return box{typ: t, data: concatBoxes(children)}
}

func concatBoxes(children []box) []byte {
	var out []byte
	for _, c := range children {
		out = append(out, c.bytes()...)
	}
	return out
}

// withChildren appends child boxes to an already-marshalled entry, adjusting
// the parent size. It is used for sample entries (avc1 + avcC, mp4a + esds).
func withChildren(entry mp4.IImmutableBox, children ...box) box {
	base := leaf(entry)
	base.data = append(base.data, concatBoxes(children)...)
	return base
}

// u32 writes a big-endian uint32.
func u32(v uint32) []byte { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }

// u64 writes a big-endian uint64.
func u64(v uint64) []byte {
	return []byte{
		byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32),
		byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v),
	}
}

// writeFile writes bytes to dir/name, creating parents, and returns the size.
func writeFile(dir, name string, data []byte) (int64, error) {
	abs := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}
