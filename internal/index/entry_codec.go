package index

import (
	"encoding/binary"
	"io"

	"github.com/zld/continuity/internal/clock"
)

// entryWireSize is the fixed on-disk length of one entry. Rational times are
// stored as numerator/denominator pairs so no precision is lost on disk.
const entryWireSize = 4 + 4 + 8 + 8 + 8 + 4 + 1 // 37 bytes

func encodeEntry(e Entry) []byte {
	buf := make([]byte, entryWireSize)
	binary.BigEndian.PutUint32(buf[0:], e.Track)
	binary.BigEndian.PutUint32(buf[4:], e.Sample)
	binary.BigEndian.PutUint64(buf[8:], uint64(e.Time.N))
	binary.BigEndian.PutUint64(buf[16:], uint64(e.Time.D))
	binary.BigEndian.PutUint64(buf[24:], e.FileOffset)
	binary.BigEndian.PutUint32(buf[32:], e.Size)
	if e.Sync {
		buf[36] = 1
	}
	return buf
}

func decodeEntry(buf []byte) (Entry, error) {
	if len(buf) < entryWireSize {
		return Entry{}, io.ErrUnexpectedEOF
	}
	return Entry{
		Track:      binary.BigEndian.Uint32(buf[0:]),
		Sample:     binary.BigEndian.Uint32(buf[4:]),
		Time:       clock.Rational{N: int64(binary.BigEndian.Uint64(buf[8:])), D: int64(binary.BigEndian.Uint64(buf[16:]))},
		FileOffset: binary.BigEndian.Uint64(buf[24:]),
		Size:       binary.BigEndian.Uint32(buf[32:]),
		Sync:       buf[36] != 0,
	}, nil
}
