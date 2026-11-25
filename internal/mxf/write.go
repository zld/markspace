package mxf

import (
	auditerrors "media-audit/internal/errors"
	"media-audit/internal/media"
	"media-audit/internal/wire"
)

func encodeBER(length int) []byte {
	if length < 0x80 {
		return []byte{byte(length)}
	}
	var raw []byte
	v := length
	for v > 0 {
		raw = append([]byte{byte(v & 0xff)}, raw...)
		v >>= 8
	}
	if len(raw) > 8 {
		panic("KLV value too large")
	}
	return append([]byte{0x80 | byte(len(raw))}, raw...)
}

// AppendKLV appends one KLV and returns its absolute-ish element descriptor;
// callers building from offset zero can use it directly.
func AppendKLV(b *wire.Buffer, key Key, value []byte) media.Element {
	start := int64(b.Len())
	b.Raw(key[:])
	ber := encodeBER(len(value))
	b.Raw(ber)
	valueStart := int64(b.Len())
	b.Raw(value)
	end := int64(b.Len())
	return media.Element{
		Type:       key.Name(),
		Offset:     start,
		HeaderSize: int(valueStart - start),
		Range:      auditerrors.ByteRange{Start: start, End: end},
	}
}

// Build is a small convenience for tests and generated fixtures.
func Build(parts []struct {
	Key   Key
	Value []byte
}) []byte {
	var b wire.Buffer
	for _, p := range parts {
		AppendKLV(&b, p.Key, p.Value)
	}
	return b.Bytes()
}
