package ebml

import "tracecheck/internal/serr"

// Builder is a minimal EBML writer used by tests and the demo fixture
// generator. It emits canonical element headers and keeps byte offsets exact.
type Builder struct {
	buf []byte
}

// Bytes returns the accumulated EBML bytes.
func (b *Builder) Bytes() []byte { return b.buf }

// Len returns the number of bytes written so far.
func (b *Builder) Len() int { return len(b.buf) }

// WriteRaw appends raw bytes (used to synthesize truncated/corrupt fixtures).
func (b *Builder) WriteRaw(p []byte) { b.buf = append(b.buf, p...) }

// Element writes an element with id and an explicitly encoded payload.
func (b *Builder) Element(id ElementID, payload []byte) error {
	return b.element(id, payload, false)
}

// UnknownElement writes id followed by a 1-byte all-ones size VINT and payload.
func (b *Builder) UnknownElement(id ElementID, payload []byte) error {
	return b.element(id, payload, true)
}

// Container writes id around the body produced by fn.
func (b *Builder) Container(id ElementID, fn func(inner *Builder) error) error {
	inner := &Builder{}
	if err := fn(inner); err != nil {
		return err
	}
	return b.Element(id, inner.Bytes())
}

func (b *Builder) element(id ElementID, payload []byte, unknown bool) error {
	idBytes, err := EncodeID(id)
	if err != nil {
		return err
	}
	b.buf = append(b.buf, idBytes...)
	if unknown {
		b.buf = append(b.buf, 0xFF)
	} else {
		size, err := EncodeVINT(uint64(len(payload)))
		if err != nil {
			return serr.New(serr.CodeIllegalVINT, "payload too large: %d", len(payload))
		}
		b.buf = append(b.buf, size...)
	}
	b.buf = append(b.buf, payload...)
	return nil
}

// UintElement writes an EBML unsigned integer payload (big endian, minimal).
func (b *Builder) UintElement(id ElementID, value uint64) error {
	if value == 0 {
		return b.Element(id, []byte{0})
	}
	var raw [8]byte
	n := 0
	for v := value; v != 0; v >>= 8 {
		raw[7-n] = byte(v & 0xFF)
		n++
	}
	return b.Element(id, raw[8-n:])
}

// FloatElement writes an EBML 64-bit big endian IEEE-754 double.
func (b *Builder) FloatElement(id ElementID, value float64) error {
	raw := float64BE(value)
	return b.Element(id, raw[:])
}

// StringElement writes an EBML ASCII/UTF-8 string element.
func (b *Builder) StringElement(id ElementID, value string) error {
	return b.Element(id, []byte(value))
}
