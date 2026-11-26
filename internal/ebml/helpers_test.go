package ebml

import (
	"io"
	"math"
)

type byteReaderAt []byte

func (b byteReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func bytesReaderAt(b []byte) byteReaderAt { return byteReaderAt(b) }

func float64bits(f float64) uint64 { return math.Float64bits(f) }
