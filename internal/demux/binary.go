package demux

import (
	"encoding/binary"

	"mediapipeline/internal/fault"
)

// binReader is a bounds-checked reader over one box payload.  Its absolute
// base is used to turn relative read positions into raw input offsets without
// ever guessing.
type binReader struct {
	b    []byte
	pos  int
	base int64 // absolute offset of b[0]
	typ  string
	fmt  string
	op   fault.Operation
}

func newBinReader(b *rawBox, op fault.Operation) *binReader {
	return &binReader{b: b.payload(), pos: 0, base: b.payloadOff(), typ: b.typ, fmt: "mp4", op: op}
}

func (r *binReader) abs() int64 { return r.base + int64(r.pos) }

func (r *binReader) fail(reason string) error {
	return fault.New(fault.StageDemux, r.fmt, r.op, r.abs(), reason)
}

func (r *binReader) need(n int) error {
	if r.pos+n > len(r.b) {
		return r.fail("payload of " + r.typ + " ends early")
	}
	return nil
}

func (r *binReader) u8() (uint8, error) {
	if err := r.need(1); err != nil {
		return 0, err
	}
	v := r.b[r.pos]
	r.pos++
	return v, nil
}

func (r *binReader) u16() (uint16, error) {
	if err := r.need(2); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint16(r.b[r.pos:])
	r.pos += 2
	return v, nil
}

func (r *binReader) u24() (uint32, error) {
	if err := r.need(3); err != nil {
		return 0, err
	}
	v := uint32(r.b[r.pos])<<16 | uint32(r.b[r.pos+1])<<8 | uint32(r.b[r.pos+2])
	r.pos += 3
	return v, nil
}

func (r *binReader) u32() (uint32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *binReader) u64() (uint64, error) {
	if err := r.need(8); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint64(r.b[r.pos:])
	r.pos += 8
	return v, nil
}

func (r *binReader) bytes(n int) ([]byte, error) {
	if n < 0 {
		return nil, r.fail("negative length")
	}
	if err := r.need(n); err != nil {
		return nil, err
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v, nil
}

func (r *binReader) skip(n int) error {
	if err := r.need(n); err != nil {
		return err
	}
	r.pos += n
	return nil
}

// fullBox reads version and flags.
func (r *binReader) fullBox() (version uint8, flags uint32, err error) {
	v, err := r.u8()
	if err != nil {
		return 0, 0, err
	}
	f, err := r.u24()
	if err != nil {
		return 0, 0, err
	}
	return v, f, nil
}
