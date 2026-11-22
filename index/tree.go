package index

import (
	"github.com/abema/go-mp4"
)

// node is the uniform return value of the structure handler: a box's info,
// typed payload and child nodes.
type node struct {
	info     mp4.BoxInfo
	payload  mp4.IBox
	children []interface{}
}

// isLeaf reports the box types that carry a typed payload and no recursively
// expanded child boxes. stsd and dref contain sample-entry / data-reference
// items (avc1, mp4a, url) whose type lives inside the parent payload rather
// than as standalone boxes, so they are treated as leaves by the structural
// walker; their typed payload is still unmarshalled by go-mp4. Every other
// box type is expanded as a container and a failure propagates as corruption.
func isLeaf(t mp4.BoxType) bool {
	switch t.String() {
	case "ftyp", "stts", "stss", "stsz", "stsc", "stco", "co64", "mdhd",
		"tkhd", "mvhd", "hdlr", "vmhd", "smhd", "url ", "urn ", "free", "skip",
		"mdat", "stsd", "dref":
		return true
	default:
		return false
	}
}

// toSlice normalizes a node (or a raw slice from ReadBoxStructure) to children.
func toSlice(v interface{}) []interface{} {
	if v == nil {
		return nil
	}
	if n, ok := v.(*node); ok {
		return n.children
	}
	if s, ok := v.([]interface{}); ok {
		return s
	}
	return nil
}

// payload extracts the typed payload from a node.
func payload(v interface{}) mp4.IBox {
	if n, ok := v.(*node); ok {
		return n.payload
	}
	return nil
}

// findBox returns the first direct child node of the given type, or nil.
func findBox(children []interface{}, t mp4.BoxType) interface{} {
	for _, c := range children {
		n, ok := c.(*node)
		if !ok {
			continue
		}
		if n.info.Type == t {
			return n
		}
	}
	return nil
}

// findAll returns every direct child node of the given type.
func findAll(v interface{}, t mp4.BoxType) []interface{} {
	out := make([]interface{}, 0)
	for _, c := range toSlice(v) {
		if n, ok := c.(*node); ok && n.info.Type == t {
			out = append(out, n)
		}
	}
	return out
}

// versionedDuration resolves the version 0/1 duration field of an mdhd/tkhd.
func versionedDuration(v0 uint32, v1 uint64, version uint8) uint64 {
	if version == 1 {
		return v1
	}
	return uint64(v0)
}

// mdhdDuration and tkhdDuration read the correct duration field for the box's
// full-box version, returning 0 on a nil payload.
func mdhdDuration(b mp4.IBox) uint64 {
	m, ok := b.(*mp4.Mdhd)
	if !ok {
		return 0
	}
	return versionedDuration(m.DurationV0, m.DurationV1, m.Version)
}

func tkhdDuration(b mp4.IBox) uint64 {
	t, ok := b.(*mp4.Tkhd)
	if !ok {
		return 0
	}
	return versionedDuration(t.DurationV0, t.DurationV1, t.Version)
}
