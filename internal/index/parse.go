// Package index expands MP4 sample tables (classic stbl and fragmented
// moof/traf) into per-sample records with offsets and timestamps.
package index

import (
	"encoding/binary"
	"fmt"

	"mp4seek/internal/bmff"
)

// MaxSamples caps expanded sample counts to bound memory.
const MaxSamples = 4 << 20

// cursor is a bounds-checked big-endian reader over a box payload.
type cursor struct {
	data []byte
	off  int
	path string
}

func (c *cursor) fail(what string) error {
	return fmt.Errorf("%w: %s: %s", bmff.ErrCorrupt, c.path, what)
}

func (c *cursor) take(n int) ([]byte, error) {
	if n < 0 || len(c.data)-c.off < n {
		return nil, c.fail(fmt.Sprintf("need %d bytes at %d, have %d", n, c.off, len(c.data)-c.off))
	}
	b := c.data[c.off : c.off+n]
	c.off += n
	return b, nil
}

func (c *cursor) u8() (uint8, error) {
	b, err := c.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (c *cursor) u16() (uint16, error) {
	b, err := c.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (c *cursor) u32() (uint32, error) {
	b, err := c.take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (c *cursor) i32() (int32, error) {
	v, err := c.u32()
	return int32(v), err
}

func (c *cursor) u64() (uint64, error) {
	b, err := c.take(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

func (c *cursor) remaining() int { return len(c.data) - c.off }

// fullBoxHeader reads version and flags from a full-box payload.
func (c *cursor) fullBoxHeader() (version uint8, flags uint32, err error) {
	b, err := c.take(4)
	if err != nil {
		return 0, 0, err
	}
	return b[0], uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]), nil
}
