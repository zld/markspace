// Package index validates keyframe indexes against parsed streams:
// offsets must be sorted, inside the stream, and point at keyframe
// units.
package index

import (
	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
)

// Index lists the stream byte offsets of keyframe units.
type Index struct {
	Offsets []int64
}

// Build derives an Index from parsed units.
func Build(units []codec.Unit) Index {
	var idx Index
	for _, u := range units {
		if u.Keyframe {
			idx.Offsets = append(idx.Offsets, u.Offset)
		}
	}
	return idx
}

// Validate checks the index against the parsed units of a stream of
// streamLen bytes. The first violation is reported with its offset.
func (idx Index) Validate(units []codec.Unit, streamLen int64) error {
	if len(idx.Offsets) == 0 {
		return fault.New(fault.ErrIndexOutOfRange, 0, "index has no keyframes")
	}
	known := make(map[int64]bool, len(units))
	for _, u := range units {
		if u.Keyframe {
			known[u.Offset] = true
		}
	}
	prev := int64(-1)
	for _, off := range idx.Offsets {
		switch {
		case off < 0 || off >= streamLen:
			return fault.New(fault.ErrIndexOutOfRange, off,
				"keyframe offset outside stream of %d bytes", streamLen)
		case off <= prev:
			return fault.New(fault.ErrIndexOutOfRange, off,
				"keyframe offsets not strictly increasing (prev %d)", prev)
		case !known[off]:
			return fault.New(fault.ErrIndexOutOfRange, off,
				"offset does not point at a keyframe unit")
		}
		prev = off
	}
	return nil
}
