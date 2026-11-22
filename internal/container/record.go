package container

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// RecordInspector validates one fixture sample record at an offset. It is the
// independent check rescan uses; it is also what tests assert against.
type RecordInspector struct {
	Limit int64
}

// Inspect returns the record length and payload framing at off, or an error
// when the bytes there are not a verifiable record.
func (ri RecordInspector) Inspect(f io.ReaderAt, off, fileSize int64, wantSeq uint32) (length int64, frames int, channels int, err error) {
	if off < 0 || off+fixtureRecordHeader > fileSize {
		return 0, 0, 0, fmt.Errorf("record offset %d outside file", off)
	}
	var hdr [fixtureRecordHeader]byte
	if _, err := f.ReadAt(hdr[:], off); err != nil {
		return 0, 0, 0, err
	}
	if binary.BigEndian.Uint16(hdr[0:]) != fixtureRecordMagic {
		return 0, 0, 0, fmt.Errorf("record magic missing at %d", off)
	}
	if hdr[2] != fixtureRecordVersion {
		return 0, 0, 0, fmt.Errorf("unsupported record version %d at %d", hdr[2], off)
	}
	ch := int(hdr[3])
	rate := binary.BigEndian.Uint32(hdr[4:])
	payload := int64(binary.BigEndian.Uint32(hdr[8:]))
	seq := binary.BigEndian.Uint32(hdr[12:])
	framesOut := int(binary.BigEndian.Uint32(hdr[16:]))
	crc := binary.BigEndian.Uint32(hdr[20:])
	if seq != wantSeq {
		return 0, 0, 0, fmt.Errorf("sequence %d at %d, want %d", seq, off, wantSeq)
	}
	if rate == 0 || rate > 1_000_000_000 || ch == 0 || ch > 32 {
		return 0, 0, 0, fmt.Errorf("record header implausible at %d", off)
	}
	if payload <= 0 || off+fixtureRecordHeader+payload > fileSize {
		return 0, 0, 0, fmt.Errorf("record payload %d truncated at %d", payload, off)
	}
	if ri.Limit > 0 && payload > ri.Limit {
		return 0, 0, 0, fmt.Errorf("record payload %d exceeds limit", payload)
	}
	if payload != int64(framesOut*ch*2) {
		return 0, 0, 0, fmt.Errorf("payload %d inconsistent with %d frames x %d ch", payload, framesOut, ch)
	}
	buf := make([]byte, payload)
	if _, err := f.ReadAt(buf, off+fixtureRecordHeader); err != nil {
		return 0, 0, 0, err
	}
	if crc32.ChecksumIEEE(buf) != crc {
		return 0, 0, 0, fmt.Errorf("record CRC mismatch at %d", off)
	}
	return fixtureRecordHeader + payload, framesOut, ch, nil
}

const (
	fixtureRecordMagic   = 0xAF01
	fixtureRecordVersion = 1
	fixtureRecordHeader  = 24 // 16  #nosec - fixed framing, not a credential
)
