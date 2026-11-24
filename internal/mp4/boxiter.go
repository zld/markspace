package mp4

import (
	"encoding/binary"

	"github.com/zld/avpipe/internal/fault"
)

// readBoxAt safely reads one box header. The returned view's payload is
// limited to bytes actually present even when the declared size is truncated.
func readBoxAt(data []byte, off int, component string) (BoxView, *fault.Error) {
	if off < 0 || off > len(data) {
		return BoxView{}, fault.OutOfBounds(component, int64(off), "box offset")
	}
	if len(data)-off < 8 {
		return BoxView{}, fault.Truncated(component, int64(off), int64(len(data)), "box header")
	}

	typ := string(data[off+4 : off+8])
	size32 := binary.BigEndian.Uint32(data[off : off+4])
	headerSize := 8
	declared := int64(size32)

	if size32 == 1 {
		if len(data)-off < 16 {
			return BoxView{}, fault.Truncated(component, int64(off+8), int64(len(data)), "largesize box header")
		}
		declared = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
		headerSize = 16
	} else if size32 == 0 {
		declared = int64(len(data) - off)
	}
	if declared < int64(headerSize) {
		return BoxView{}, fault.New(fault.KindInvalid, "box %s has invalid size %d", typ, declared).
			At(component, int64(off), 0)
	}

	declaredEnd := int64(off) + declared
	complete := declaredEnd <= int64(len(data))
	availableEnd := len(data)
	if complete {
		availableEnd = int(declaredEnd)
	}
	payloadStart := off + headerSize
	if payloadStart > availableEnd {
		return BoxView{}, fault.Truncated(component, int64(off), int64(len(data)), typ+" payload header")
	}

	return BoxView{
		Type:         typ,
		Offset:       off,
		HeaderSize:   headerSize,
		DeclaredSize: int(declared),
		PayloadStart: payloadStart,
		Payload:      data[payloadStart:availableEnd:availableEnd],
		Complete:     complete,
	}, nil
}

// iterBoxes invokes fn for every readable child box in b. A truncated child
// stops iteration and returns its structured fault.
func iterBoxes(b []byte, baseOffset int, fn func(BoxView) error) error {
	off := 0
	for off < len(b) {
		v, err := readBoxAt(b, off, "mp4/child")
		if err != nil {
			return err
		}
		// Payload offsets are relative to b; callers that need global offsets
		// can add baseOffset.
		v.Offset += baseOffset
		v.PayloadStart += baseOffset
		if err := fn(v); err != nil {
			return err
		}
		if v.DeclaredSize <= 0 {
			return fault.New(fault.KindInvalid, "zero advancement in box %s", v.Type)
		}
		off += v.HeaderSize + len(v.Payload)
		// If the box was truncated, len(payload) reaches the end. Stop to
		// avoid interpreting trailing partial bytes as another box.
		if !v.Complete {
			return fault.Truncated("mp4/child", int64(v.Offset), int64(baseOffset+len(b)), v.Type+" box")
		}
		// Correct advancement should use declared size; payload length equals
		// declared-header when complete. The expression above is equivalent.
	}
	return nil
}

// findChild returns the first direct child of a box payload.
func findChild(payload []byte, want string) (BoxView, bool) {
	var found BoxView
	ok := false
	_ = iterBoxes(payload, 0, func(v BoxView) error {
		if v.Type == want && !ok {
			found = v
			ok = true
		}
		return nil
	})
	return found, ok
}

// findDeep recursively searches a box payload for want. The returned view's
// offsets are relative to payload; callers parsing only Payload do not need
// global offsets.
func findDeep(payload []byte, want string) (BoxView, bool) {
	if v, ok := findChild(payload, want); ok {
		return v, true
	}
	var result BoxView
	found := false
	_ = iterBoxes(payload, 0, func(parent BoxView) error {
		if found {
			return nil
		}
		if child, ok := findDeep(parent.Payload, want); ok {
			result = child
			found = true
		}
		return nil
	})
	return result, found
}
