package ebml

import "io"

// 基于字节切片的元素读取辅助。

type byteSliceReader struct {
	b   []byte
	off int
}

func bytesReader(b []byte) *byteSliceReader { return &byteSliceReader{b: b} }

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}

func readHeaderFrom(c *countingReader) (*Element, error) {
	off := c.pos
	id, _, err := readVINT(c, true)
	if err != nil {
		return nil, err
	}
	sz, _, err := readVINT(c, false)
	if err != nil {
		return nil, err
	}
	return &Element{ID: uint32(id), Size: int64(sz), Offset: off, DataOffset: c.pos}, nil
}

func readBytesFrom(c *countingReader, n int64) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(c, buf)
	return buf, err
}

func skipN(c *countingReader, n int64) { io.CopyN(io.Discard, c, n) }

func uintFromBytes(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}
