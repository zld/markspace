package ebml

import (
	"encoding/binary"
	"errors"
	"io"
)

// readFullFrom isolates io.ReadFull so the VINT code stays free of direct
// reader plumbing.
func readFullFrom(r io.Reader, buf []byte) (int, error) {
	return io.ReadFull(r, buf)
}

// isEOF reports either flavor of end-of-file the standard reader returns.
func isEOF(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func bigEndianUint32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
func bigEndianUint64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }
