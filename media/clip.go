package media

import (
	"fmt"

	"mediad/index"
)

// RawBox is an out-of-band metadata box located in the file.
type RawBox struct {
	Offset  int64
	Size    int64
	Payload []byte
	// BaseTime is the segment base time for v0 emsg boxes, zero otherwise.
	BaseTime uint64
}

// ChplBoxes returns the Nero chapter lists found at the top level or under
// moov/udta, with their payload and byte offset.
func (c *Clip) ChplBoxes() []RawBox { return c.chpl }

// EmsgBoxes returns the emsg boxes found at the top level.
func (c *Clip) EmsgBoxes() []RawBox { return c.emsg }

// SampleData returns the bytes of one sample as located by the index. A sample
// whose range falls outside the retained bytes is an error, not truncated data.
func (c *Clip) SampleData(s index.SampleIndex) ([]byte, error) {
	if s.Offset < 0 || s.Size < 0 || s.Offset+s.Size > int64(len(c.data)) {
		return nil, fmt.Errorf("sample [%d,%d) outside %d-byte file", s.Offset, s.Offset+s.Size, len(c.data))
	}
	return c.data[s.Offset : s.Offset+s.Size], nil
}

// Bytes returns the original file bytes (for fingerprinting without re-reading).
func (c *Clip) Bytes() []byte { return c.data }
