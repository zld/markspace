// Package movie parses the moov box (tracks, media, sample tables) into
// a typed model, reusing the box boundary layer for all reads.
package movie

import (
	"encoding/binary"
	"fmt"
)

// cursor bounds-checks sequential reads over a box payload.
type cursor struct {
	b   []byte
	off int
	typ string
}

func (c *cursor) take(n int) ([]byte, error) {
	if n < 0 || len(c.b)-c.off < n {
		return nil, fmt.Errorf("%s: truncated at payload offset %d (need %d, have %d)", c.typ, c.off, n, len(c.b)-c.off)
	}
	p := c.b[c.off : c.off+n]
	c.off += n
	return p, nil
}

func (c *cursor) u8() (uint8, error) {
	p, err := c.take(1)
	if err != nil {
		return 0, err
	}
	return p[0], nil
}

func (c *cursor) u16() (uint16, error) {
	p, err := c.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(p), nil
}

func (c *cursor) u32() (uint32, error) {
	p, err := c.take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(p), nil
}

func (c *cursor) u64() (uint64, error) {
	p, err := c.take(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(p), nil
}

func (c *cursor) skip(n int) error {
	_, err := c.take(n)
	return err
}
