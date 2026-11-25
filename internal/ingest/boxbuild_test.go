package ingest

import (
	"bytes"
	"encoding/binary"
)

type boxBuilder struct{ b bytes.Buffer }

func newBoxBuilder() *boxBuilder { return &boxBuilder{} }

func (bb *boxBuilder) box(typ string, parts ...[]byte) {
	body := bytes.Join(parts, nil)
	binary.Write(&bb.b, binary.BigEndian, uint32(8+len(body)))
	bb.b.WriteString(typ)
	bb.b.Write(body)
}

func (bb *boxBuilder) fullBox(typ string, version uint8, flags uint32, parts ...[]byte) {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	bb.box(typ, append([][]byte{head}, parts...)...)
}

func (bb *boxBuilder) bytes() []byte { return bb.b.Bytes() }

func boxBytes(typ string, payload []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(8+len(payload)))
	b.WriteString(typ)
	b.Write(payload)
	return b.Bytes()
}

func u16b(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32b(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

func matrix() []byte {
	return bytes.Join([][]byte{
		u32b(0x10000), u32b(0), u32b(0),
		u32b(0), u32b(0x10000), u32b(0),
		u32b(0), u32b(0), u32b(0x40000000),
	}, nil)
}
